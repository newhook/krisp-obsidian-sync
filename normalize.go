package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"google.golang.org/genai"
)

//go:embed normalize-prompt.md
var normalizePromptTemplate string

const tagsDictionaryFile = "tags-dictionary.json"

// TagsDictionary holds the canonical tag set and mappings
type TagsDictionary struct {
	CanonicalTags  []string          `json:"canonical_tags"`
	Mappings       map[string]string `json:"mappings"` // old tag -> canonical tag
	LastNormalized time.Time         `json:"last_normalized"`
	MeetingCount   int               `json:"meeting_count"`
}

// Stage 4: Normalize tags across all cached summaries
func runNormalize(ctx context.Context, cache *Cache) error {
	fmt.Println("\n=== Stage 4: Normalizing tags ===")

	// Get all cached summary files
	files, err := filepath.Glob(filepath.Join(meetingsCacheDir, "*-summary.json"))
	if err != nil {
		return fmt.Errorf("error reading cache directory: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("⚠ No cached summaries found. Run summarize step first.")
		return nil
	}

	fmt.Printf("Found %d meeting summaries to analyze\n", len(files))

	// Load all summaries and collect tag counts
	type meetingSummary struct {
		MeetingID   string
		SummaryData *SummaryData
	}

	summaries := make([]meetingSummary, 0, len(files))
	tagCounts := make(map[string]int)

	for _, file := range files {
		// Check if context was cancelled
		if ctx.Err() != nil {
			fmt.Printf("\n⚠ Normalization cancelled\n")
			return ctx.Err()
		}

		meetingID := strings.TrimSuffix(filepath.Base(file), "-summary.json")

		summaryData, err := cache.LoadSummary(meetingID)
		if err != nil {
			fmt.Printf("⚠ Error loading summary for %s: %v\n", meetingID, err)
			continue
		}

		summaries = append(summaries, meetingSummary{
			MeetingID:   meetingID,
			SummaryData: summaryData,
		})

		// Parse tags (comma-separated)
		if summaryData.Tags != "" {
			tags := strings.Split(summaryData.Tags, ",")
			for _, tag := range tags {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					tagCounts[tag]++
				}
			}
		}
	}

	if len(tagCounts) == 0 {
		fmt.Println("⚠ No tags found in summaries")
		return nil
	}

	fmt.Printf("📊 Found %d unique tags across all meetings\n", len(tagCounts))

	// Consolidate tags using LLM
	fmt.Println("🤖 Consolidating tags with Gemini...")
	canonicalTags, mappings, err := consolidateTagsWithLLM(ctx, tagCounts)
	if err != nil {
		return fmt.Errorf("error consolidating tags: %w", err)
	}

	fmt.Printf("✓ Consolidated to %d canonical tags\n", len(canonicalTags))

	// Update all summary files with canonical tags
	fmt.Println("📝 Updating cached summaries with canonical tags...")
	updatedCount := 0
	for _, ms := range summaries {
		// Apply tag mappings
		if ms.SummaryData.Tags != "" {
			tags := strings.Split(ms.SummaryData.Tags, ",")
			var normalizedTags []string
			for _, tag := range tags {
				tag = strings.TrimSpace(tag)
				if canonical, ok := mappings[tag]; ok {
					normalizedTags = append(normalizedTags, canonical)
				} else {
					normalizedTags = append(normalizedTags, tag)
				}
			}

			// Remove duplicates and sort
			normalizedTags = unique(normalizedTags)
			sort.Strings(normalizedTags)

			ms.SummaryData.Tags = strings.Join(normalizedTags, ", ")

			if err := cache.SaveSummary(ms.MeetingID, ms.SummaryData); err != nil {
				fmt.Printf("⚠ Error updating summary for %s: %v\n", ms.MeetingID, err)
				continue
			}

			updatedCount++
		}
	}

	fmt.Printf("✓ Updated %d summaries\n", updatedCount)

	// Save tags dictionary
	dictionary := &TagsDictionary{
		CanonicalTags:  canonicalTags,
		Mappings:       mappings,
		LastNormalized: time.Now(),
		MeetingCount:   len(files),
	}

	if err := saveTagsDictionary(dictionary); err != nil {
		return fmt.Errorf("error saving tags dictionary: %w", err)
	}

	fmt.Printf("✓ Saved tags dictionary: %s\n", tagsDictionaryFile)
	fmt.Printf("\n✅ Normalization complete! %d canonical tags established.\n", len(canonicalTags))

	return nil
}

func consolidateTagsWithLLM(ctx context.Context, tagCounts map[string]int) ([]string, map[string]string, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  gcpProject,
		Location: gcpLocation,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Vertex AI client: %w", err)
	}

	// Build tag list sorted by frequency
	type tagInfo struct {
		Tag   string
		Count int
	}
	var tagList []tagInfo
	for tag, count := range tagCounts {
		tagList = append(tagList, tagInfo{Tag: tag, Count: count})
	}
	sort.Slice(tagList, func(i, j int) bool {
		return tagList[i].Count > tagList[j].Count
	})

	// Parse the normalize prompt template
	tmpl, err := template.New("normalize").Parse(normalizePromptTemplate)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse normalize template: %w", err)
	}

	// Execute template with tag data
	var promptBuf bytes.Buffer
	if err := tmpl.Execute(&promptBuf, map[string]interface{}{"Tags": tagList}); err != nil {
		return nil, nil, fmt.Errorf("failed to execute normalize template: %w", err)
	}
	prompt := promptBuf.String()

	// Define JSON schema for response
	schema := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"canonical_tags": {
				Type:        genai.TypeArray,
				Description: "List of canonical tags to use",
				Items: &genai.Schema{
					Type: genai.TypeString,
				},
			},
			"mappings": {
				Type:        genai.TypeObject,
				Description: "Map of old tags to canonical tags (only include tags that need mapping)",
			},
		},
		Required: []string{"canonical_tags", "mappings"},
	}

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.0-flash-lite", []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				genai.NewPartFromText(prompt),
			},
		},
	}, &genai.GenerateContentConfig{
		Temperature:      func() *float32 { v := float32(0.3); return &v }(),
		MaxOutputTokens:  4096,
		ResponseMIMEType: "application/json",
		ResponseSchema:   schema,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to consolidate tags: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, nil, fmt.Errorf("no response generated")
	}

	responseText := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0].Text)

	var result struct {
		CanonicalTags []string          `json:"canonical_tags"`
		Mappings      map[string]string `json:"mappings"`
	}

	if err := json.Unmarshal([]byte(responseText), &result); err != nil {
		return nil, nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.CanonicalTags, result.Mappings, nil
}

func saveTagsDictionary(dict *TagsDictionary) error {
	data, err := json.MarshalIndent(dict, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal dictionary: %w", err)
	}

	if err := os.WriteFile(tagsDictionaryFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write dictionary file: %w", err)
	}

	return nil
}

func loadTagsDictionary() (*TagsDictionary, error) {
	data, err := os.ReadFile(tagsDictionaryFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No dictionary yet
		}
		return nil, fmt.Errorf("failed to read dictionary: %w", err)
	}

	var dict TagsDictionary
	if err := json.Unmarshal(data, &dict); err != nil {
		return nil, fmt.Errorf("failed to parse dictionary: %w", err)
	}

	return &dict, nil
}

// unique removes duplicates from a string slice
func unique(slice []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, val := range slice {
		if !seen[val] {
			seen[val] = true
			result = append(result, val)
		}
	}
	return result
}
