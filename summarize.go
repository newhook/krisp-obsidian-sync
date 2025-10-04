package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"

	"google.golang.org/genai"
)

//go:embed summary-prompt.md
var summaryPromptTemplate string

// Stage 2: Summarize cached meetings with Gemini
func runSummarize(ctx context.Context, limit int, syncState *SyncState, resummarize bool, cache *Cache) error {
	fmt.Println("\n=== Stage 2: Summarizing meetings ===")

	if resummarize {
		fmt.Println("🔄 Resummarize mode: clearing summarization state")
		syncState.SummarizedMeetings = make(map[string]bool)
	}

	// Load tags dictionary if it exists
	dict, err := loadTagsDictionary()
	var existingTags []string
	if err == nil && dict != nil {
		existingTags = dict.CanonicalTags
		fmt.Printf("📚 Loaded %d canonical tags from dictionary\n", len(existingTags))
	} else {
		fmt.Println("📝 No tags dictionary found - tags will be generated freely")
	}

	// Get meetings from sync state that need summarization
	if len(syncState.SyncedMeetings) == 0 {
		fmt.Println("⚠ No cached meetings found. Run download step first.")
		return nil
	}

	// Load meetings that need summarization and sort by creation time
	type meetingToSummarize struct {
		ID        string
		CreatedAt time.Time
	}

	var toSummarize []meetingToSummarize
	for meetingID := range syncState.SyncedMeetings {
		if !syncState.SummarizedMeetings[meetingID] {
			// Load meeting to get creation time for sorting
			meeting, err := cache.LoadMeeting(meetingID)
			if err != nil {
				fmt.Printf("⚠ Error loading meeting %s for sorting: %v\n", meetingID, err)
				continue
			}
			toSummarize = append(toSummarize, meetingToSummarize{
				ID:        meetingID,
				CreatedAt: meeting.CreatedAt,
			})
		}
	}

	if len(toSummarize) == 0 {
		fmt.Println("✅ All cached meetings already summarized!")
		return nil
	}

	// Sort by creation time (oldest first)
	sort.Slice(toSummarize, func(i, j int) bool {
		return toSummarize[i].CreatedAt.Before(toSummarize[j].CreatedAt)
	})

	fmt.Printf("Found %d meeting(s) to summarize (oldest to newest)\n", len(toSummarize))

	// Apply limit
	if limit > 0 && len(toSummarize) > limit {
		fmt.Printf("⚠ Limiting to %d meeting(s) for this run\n", limit)
		toSummarize = toSummarize[:limit]
	}

	// Summarize each meeting
	for i, m := range toSummarize {
		// Check if context was cancelled
		if ctx.Err() != nil {
			fmt.Printf("\n⚠ Summarization cancelled\n")
			return ctx.Err()
		}

		fmt.Printf("[%d/%d] Summarizing meeting: %s\n", i+1, len(toSummarize), m.ID)

		// Load from cache (might already be cached from sorting)
		meeting, err := cache.LoadMeeting(m.ID)
		if err != nil {
			fmt.Printf("  ⚠ Error loading from cache: %v\n", err)
			continue
		}

		// Parse transcript
		var transcriptText string
		if meeting.Resources.Transcript.Status == "uploaded" && meeting.Resources.Transcript.Content != "" {
			var segments []Segment
			if err := json.Unmarshal([]byte(meeting.Resources.Transcript.Content), &segments); err != nil {
				fmt.Printf("  ⚠ Error parsing transcript: %v\n", err)
			} else if len(segments) > 0 {
				var sb strings.Builder
				for _, seg := range segments {
					sb.WriteString(fmt.Sprintf("Speaker %d: %s\n", seg.SpeakerIndex, seg.Speech.Text))
				}
				transcriptText = sb.String()
			}
		}

		if transcriptText == "" {
			fmt.Printf("  ⚠ No transcript available\n")
			continue
		}

		// Generate summary with Gemini
		summaryResponse, err := summarizeWithGemini(ctx, transcriptText, existingTags)
		if err != nil {
			fmt.Printf("  ⚠ Error generating summary: %v\n", err)
			continue
		}

		// Parse the summary response to SummaryData
		summaryData := parseSummaryResponse(summaryResponse)

		// Save summary to cache
		if err := cache.SaveSummary(m.ID, summaryData); err != nil {
			fmt.Printf("  ⚠ Error saving summary: %v\n", err)
			continue
		}

		syncState.SummarizedMeetings[m.ID] = true
		fmt.Printf("  ✓ Summary saved: meetings/%s-summary.json\n", m.ID)

		// Save state after each summary
		if err := syncState.Save(); err != nil {
			fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
		}

		// Be nice to the API
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Printf("\n✅ Summarized %d meeting(s)\n", len(toSummarize))
	return nil
}

func summarizeWithGemini(ctx context.Context, transcript string, existingTags []string) (string, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  gcpProject,
		Location: gcpLocation,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create Vertex AI client: %w", err)
	}

	// Parse the summary prompt template
	tmpl, err := template.New("prompt").Parse(summaryPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}

	// Execute template with transcript data
	var promptBuf bytes.Buffer
	if err := tmpl.Execute(&promptBuf, map[string]string{"Transcript": transcript}); err != nil {
		return "", fmt.Errorf("failed to execute prompt template: %w", err)
	}
	prompt := promptBuf.String()

	// Add existing tags guidance if available
	if len(existingTags) > 0 {
		prompt += fmt.Sprintf("\n\nPrefer using these existing tags when appropriate:\n%s\n\nYou may suggest new tags if none of these fit well.", strings.Join(existingTags, ", "))
	}

	// Define JSON schema for structured output
	schema := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"description": {
				Type:        genai.TypeString,
				Description: "One-line description of the meeting",
			},
			"tags": {
				Type:        genai.TypeArray,
				Description: "List of relevant tags/keywords",
				Items: &genai.Schema{
					Type: genai.TypeString,
				},
			},
			"topics": {
				Type:        genai.TypeArray,
				Description: "List of topics discussed",
				Items: &genai.Schema{
					Type: genai.TypeString,
				},
			},
			"topic_details": {
				Type:        genai.TypeArray,
				Description: "Detailed paragraphs for each topic",
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"topic": {
							Type:        genai.TypeString,
							Description: "Topic name",
						},
						"summary": {
							Type:        genai.TypeString,
							Description: "One paragraph summary including key points, decisions, and action items",
						},
					},
					Required: []string{"topic", "summary"},
				},
			},
		},
		Required: []string{"description", "tags", "topics", "topic_details"},
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
		MaxOutputTokens:  2048,
		ResponseMIMEType: "application/json",
		ResponseSchema:   schema,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no summary generated")
	}

	summary := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0].Text)
	return summary, nil
}

// parseSummaryResponse parses the JSON response from the LLM
func parseSummaryResponse(response string) *SummaryData {
	var data struct {
		Description  string   `json:"description"`
		Tags         []string `json:"tags"`
		Topics       []string `json:"topics"`
		TopicDetails []struct {
			Topic   string `json:"topic"`
			Summary string `json:"summary"`
		} `json:"topic_details"`
	}

	if err := json.Unmarshal([]byte(response), &data); err != nil {
		fmt.Printf("  ⚠ Error parsing JSON response: %v\n", err)
		// Fallback to raw response
		return &SummaryData{
			Description: "",
			Tags:        "",
			Summary:     response,
		}
	}

	// Build the formatted summary
	var sb strings.Builder

	// Topics Discussed section
	sb.WriteString("## Topics Discussed\n")
	for _, topic := range data.Topics {
		sb.WriteString(fmt.Sprintf("- %s\n", topic))
	}
	sb.WriteString("\n")

	// Detailed topic sections
	for _, detail := range data.TopicDetails {
		sb.WriteString(fmt.Sprintf("## %s\n", detail.Topic))
		sb.WriteString(detail.Summary)
		sb.WriteString("\n\n")
	}

	return &SummaryData{
		Description: data.Description,
		Tags:        strings.Join(data.Tags, ", "),
		Summary:     sb.String(),
	}
}
