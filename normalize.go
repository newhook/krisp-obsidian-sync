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

	"github.com/lithammer/fuzzysearch/fuzzy"
)

//go:embed normalize-prompt.md
var normalizePromptTemplate string

// tagInfo represents a tag with its occurrence count
type tagInfo struct {
	Tag   string
	Count int
}

// Stage 4.1: Generate normalization prompt
func runNormalizePrompt(ctx context.Context, cache *Cache) error {
	fmt.Println("\n=== Stage 4.1: Generate Normalization Prompt ===")

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

	// Build tag list sorted by frequency
	var tagList []tagInfo
	for tag, count := range tagCounts {
		tagList = append(tagList, tagInfo{Tag: tag, Count: count})
	}
	sort.Slice(tagList, func(i, j int) bool {
		return tagList[i].Count > tagList[j].Count
	})

	// Pre-process with fuzzy matching to consolidate obvious duplicates
	fmt.Println("\n🔍 Pre-processing with fuzzy matching...")
	tagList, preMappings := fuzzyPreProcess(tagList)
	fmt.Printf("✓ Fuzzy matching reduced %d tags to %d (%.1f%% reduction)\n",
		len(tagCounts), len(tagList), (1-float64(len(tagList))/float64(len(tagCounts)))*100)

	// Save fuzzy pre-mappings for later use
	preMappingsData, err := json.MarshalIndent(preMappings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal pre-mappings: %w", err)
	}
	if err := os.WriteFile("normalize-premappings.json", preMappingsData, 0644); err != nil {
		return fmt.Errorf("failed to write pre-mappings: %w", err)
	}

	// Generate prompt for LLM
	fmt.Println("\n📝 Generating normalization prompt...")
	prompt, err := generateNormalizePrompt(tagList)
	if err != nil {
		return fmt.Errorf("failed to generate prompt: %w", err)
	}

	if err := os.WriteFile("normalize-prompt-generated.txt", []byte(prompt), 0644); err != nil {
		return fmt.Errorf("failed to write prompt file: %w", err)
	}

	fmt.Println("\n✅ Normalization prompt generated!")
	fmt.Printf("   - Pre-mappings saved to: normalize-premappings.json\n")
	fmt.Printf("   - Prompt saved to: normalize-prompt-generated.txt\n")
	fmt.Printf("   - %d tags to consolidate\n", len(tagList))
	fmt.Println("\nNext: Run your LLM on the prompt and save result to normalize-result.json")

	return nil
}

// generateNormalizePrompt creates the normalization prompt from tag list
func generateNormalizePrompt(tagList []tagInfo) (string, error) {
	tmpl, err := template.New("normalize").Parse(normalizePromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse normalize template: %w", err)
	}

	var promptBuf bytes.Buffer
	if err := tmpl.Execute(&promptBuf, map[string]interface{}{"Tags": tagList}); err != nil {
		return "", fmt.Errorf("failed to execute normalize template: %w", err)
	}

	return promptBuf.String(), nil
}

/* OLD CODE - Multi-pass chunked consolidation (COMMENTED OUT FOR NOW)
// Iteratively consolidate tags until no more consolidation happens
const chunkSize = 50              // Reduced to avoid LLM hallucination
const minConsolidationRatio = 0.9 // Stop if we're keeping > 90% of tags (not much consolidation)

currentTags := tagList
allMappings := make(map[string][]string)
passNum := 1

for {
	numChunks := (len(currentTags) + chunkSize - 1) / chunkSize
	fmt.Printf("\n🤖 Pass %d: Consolidating %d tags in %d chunk(s)...\n", passNum, len(currentTags), numChunks)

	var passCanonicalTags []string
	passMappings := make(map[string][]string)
	var unmappedTags []tagInfo // Tags that weren't mapped by LLM

	// Prepare chunks
	type chunkResult struct {
		index         int
		canonicalTags []string
		mappings      map[string][]string
		unmappedTags  []tagInfo
		err           error
	}

	var chunks [][]tagInfo
	for i := 0; i < len(currentTags); i += chunkSize {
		end := i + chunkSize
		if end > len(currentTags) {
			end = len(currentTags)
		}
		chunks = append(chunks, currentTags[i:end])
	}

	// Process chunks in parallel
	const maxConcurrency = 5
	semaphore := make(chan struct{}, maxConcurrency)
	results := make(chan chunkResult, len(chunks))

	for chunkIdx, chunk := range chunks {
		semaphore <- struct{}{}

		go func(idx int, chunkTags []tagInfo) {
			defer func() { <-semaphore }()

			fmt.Printf("  [%d/%d] Processing %d tags...\n", idx+1, len(chunks), len(chunkTags))

			chunkTagCounts := make(map[string]int)
			for _, t := range chunkTags {
				chunkTagCounts[t.Tag] = t.Count
			}

			// Retry up to 3 times
			var canonicalTags []string
			var mappings map[string][]string
			var err error
			var chunkUnmapped []tagInfo

			for attempt := 1; attempt <= 3; attempt++ {
				canonicalTags, mappings, err = consolidateTagsWithLLM(ctx, chunkTagCounts)
				if err == nil {
					fmt.Printf("  📊 Chunk %d: LLM returned %d canonical tags with %d mappings\n", idx+1, len(canonicalTags), len(mappings))

					// Check for missing tags
					mappedTags := make(map[string]bool)
					for _, canonical := range canonicalTags {
						mappedTags[canonical] = true
					}
					for _, oldTags := range mappings {
						for _, tag := range oldTags {
							mappedTags[tag] = true
						}
					}

					missingCount := 0
					for _, t := range chunkTags {
						if !mappedTags[t.Tag] {
							chunkUnmapped = append(chunkUnmapped, t)
							missingCount++
						}
					}
					if missingCount > 0 {
						fmt.Printf("  ⚠ Chunk %d: %d tags missing, will retry in next pass\n", idx+1, missingCount)
					}
					break
				}
				if attempt < 3 {
					fmt.Printf("  ⚠ Chunk %d attempt %d failed: %v, retrying...\n", idx+1, attempt, err)
				}
			}

			results <- chunkResult{
				index:         idx,
				canonicalTags: canonicalTags,
				mappings:      mappings,
				unmappedTags:  chunkUnmapped,
				err:           err,
			}
		}(chunkIdx, chunk)
	}

	// Collect results
	for i := 0; i < len(chunks); i++ {
		res := <-results
		if res.err != nil {
			return fmt.Errorf("error consolidating chunk %d in pass %d: %w", res.index+1, passNum, res.err)
		}

		passCanonicalTags = append(passCanonicalTags, res.canonicalTags...)
		unmappedTags = append(unmappedTags, res.unmappedTags...)
		for canonical, oldTags := range res.mappings {
			passMappings[canonical] = append(passMappings[canonical], oldTags...)
		}
		fmt.Printf("  ✓ Chunk %d complete: %d canonical tags\n", res.index+1, len(res.canonicalTags))
	}

	// Add unmapped tags to the canonical list for next pass
	if len(unmappedTags) > 0 {
		fmt.Printf("  ⚠ %d tag(s) not mapped by LLM, will retry in next pass\n", len(unmappedTags))
		for _, t := range unmappedTags {
			passCanonicalTags = append(passCanonicalTags, t.Tag)
		}
	}

	// Check if we made meaningful progress
	inputTagCount := len(currentTags)
	outputTagCount := len(passCanonicalTags)
	consolidationRatio := float64(outputTagCount) / float64(inputTagCount)

	fmt.Printf("✓ Pass %d complete: %d → %d canonical tags (%.1f%% reduction)\n",
		passNum, inputTagCount, outputTagCount, (1-consolidationRatio)*100)

	// Update global mappings
	if passNum == 1 {
		// First pass: direct mappings
		allMappings = passMappings
	} else {
		// Subsequent passes: chain mappings (canonical -> [intermediates] -> [originals])
		newMappings := make(map[string][]string)

		// For each new canonical tag in this pass
		for newCanonical, intermediates := range passMappings {
			var allOriginals []string
			for _, intermediate := range intermediates {
				// Check if this intermediate was itself a canonical tag with mappings
				if originals, ok := allMappings[intermediate]; ok {
					allOriginals = append(allOriginals, originals...)
				} else {
					allOriginals = append(allOriginals, intermediate)
				}
			}
			newMappings[newCanonical] = allOriginals
		}

		// Keep any mappings from previous pass that weren't consolidated in this pass
		for canonical, originals := range allMappings {
			// Check if this canonical tag still exists (wasn't consolidated)
			stillExists := false
			for newCanonical := range passMappings {
				if canonical == newCanonical {
					stillExists = true
					break
				}
			}
			if stillExists && newMappings[canonical] == nil {
				newMappings[canonical] = originals
			}
		}

		allMappings = newMappings
	}

	// Stop if we're not consolidating much anymore
	if consolidationRatio >= minConsolidationRatio {
		fmt.Printf("⚠ Minimal consolidation (%.1f%% kept), stopping\n", consolidationRatio*100)
		// Use the output from this pass as final
		currentTags = make([]tagInfo, len(passCanonicalTags))
		for i, tag := range passCanonicalTags {
			currentTags[i] = tagInfo{Tag: tag, Count: 1}
		}
		break
	}

	// Prepare for next pass
	currentTags = make([]tagInfo, len(passCanonicalTags))
	for i, tag := range passCanonicalTags {
		currentTags[i] = tagInfo{Tag: tag, Count: 1} // Equal weight
	}

	passNum++
}
... (rest of old multi-pass code)
*/
// END OLD CODE

// fuzzyPreProcess consolidates tags using fuzzy matching for obvious duplicates
// Returns consolidated tag list and mappings (canonical -> [originals])
func fuzzyPreProcess(tags []tagInfo) ([]tagInfo, map[string][]string) {
	type tagGroup struct {
		canonical  string
		tags       []tagInfo
		totalCount int
	}

	groups := make(map[string]*tagGroup)
	mappings := make(map[string][]string)

	for _, tag := range tags {
		normalized := strings.ToLower(strings.ReplaceAll(tag.Tag, "-", ""))

		// Check for exact match on normalized form (catches case and hyphen variations)
		var matchedGroup *tagGroup
		for _, group := range groups {
			groupNorm := strings.ToLower(strings.ReplaceAll(group.canonical, "-", ""))

			// Exact normalized match
			if normalized == groupNorm {
				matchedGroup = group
				break
			}

			// Fuzzy match with high threshold (Levenshtein distance <= 2 for similar length)
			if len(normalized) > 3 && fuzzy.LevenshteinDistance(normalized, groupNorm) <= 2 {
				// Only match if they're similar length (within 20%)
				lenDiff := abs(len(normalized) - len(groupNorm))
				maxLen := maxInt(len(normalized), len(groupNorm))
				if float64(lenDiff)/float64(maxLen) <= 0.2 {
					matchedGroup = group
					break
				}
			}

			// Enhanced singular/plural matching
			if isSingularPlural(normalized, groupNorm) {
				matchedGroup = group
				break
			}

			// Check for common verb/noun variations (e.g., "planning" vs "plan", "testing" vs "test")
			if isVerbNounVariation(normalized, groupNorm) {
				matchedGroup = group
				break
			}

			// Check if one is a substring of the other (with constraints)
			// e.g., "api" vs "api-integration", but only if one is significantly shorter
			if len(normalized) > 3 && len(groupNorm) > 3 {
				shorter := normalized
				longer := groupNorm
				if len(groupNorm) < len(normalized) {
					shorter = groupNorm
					longer = normalized
				}
				// If shorter is at least 4 chars and appears at start of longer
				if len(shorter) >= 4 && strings.HasPrefix(longer, shorter) {
					// Only match if the extension is common (like "ing", "s", "tion", etc)
					suffix := strings.TrimPrefix(longer, shorter)
					if isCommonSuffix(suffix) {
						matchedGroup = group
						break
					}
				}
			}
		}

		if matchedGroup != nil {
			// Add to existing group
			matchedGroup.tags = append(matchedGroup.tags, tag)
			matchedGroup.totalCount += tag.Count
		} else {
			// Create new group
			groups[tag.Tag] = &tagGroup{
				canonical:  tag.Tag,
				tags:       []tagInfo{tag},
				totalCount: tag.Count,
			}
		}
	}

	// Build consolidated list and mappings
	var consolidated []tagInfo
	for _, group := range groups {
		consolidated = append(consolidated, tagInfo{
			Tag:   group.canonical,
			Count: group.totalCount,
		})

		// Only create mapping if we actually consolidated multiple tags
		if len(group.tags) > 1 {
			var originals []string
			for _, t := range group.tags {
				if t.Tag != group.canonical {
					originals = append(originals, t.Tag)
				}
			}
			if len(originals) > 0 {
				mappings[group.canonical] = originals
			}
		}
	}

	// Sort by frequency
	sort.Slice(consolidated, func(i, j int) bool {
		return consolidated[i].Count > consolidated[j].Count
	})

	return consolidated, mappings
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isSingularPlural checks if two normalized strings are singular/plural variations
func isSingularPlural(a, b string) bool {
	// Simple plural: just add 's'
	if a+"s" == b || b+"s" == a {
		return true
	}
	// -es plural (e.g., "batch" -> "batches")
	if a+"es" == b || b+"es" == a {
		return true
	}
	// -ies plural (e.g., "query" -> "queries")
	if len(a) > 2 && len(b) > 2 {
		if strings.HasSuffix(a, "y") && strings.TrimSuffix(a, "y")+"ies" == b {
			return true
		}
		if strings.HasSuffix(b, "y") && strings.TrimSuffix(b, "y")+"ies" == a {
			return true
		}
	}
	return false
}

// isVerbNounVariation checks for common verb/noun variations
func isVerbNounVariation(a, b string) bool {
	// -ing variations (e.g., "plan" -> "planning")
	if strings.HasSuffix(a, "ing") {
		base := strings.TrimSuffix(a, "ing")
		// Handle doubled consonants (e.g., "running" -> "run")
		if len(base) > 0 && base == b {
			return true
		}
		if len(base) > 1 && base[:len(base)-1] == b {
			return true
		}
	}
	if strings.HasSuffix(b, "ing") {
		base := strings.TrimSuffix(b, "ing")
		if len(base) > 0 && base == a {
			return true
		}
		if len(base) > 1 && base[:len(base)-1] == a {
			return true
		}
	}

	// -ed variations (e.g., "deploy" -> "deployed")
	if strings.HasSuffix(a, "ed") && strings.TrimSuffix(a, "ed") == b {
		return true
	}
	if strings.HasSuffix(b, "ed") && strings.TrimSuffix(b, "ed") == a {
		return true
	}

	return false
}

// isCommonSuffix checks if a string is a common suffix that indicates related tags
func isCommonSuffix(s string) bool {
	commonSuffixes := []string{
		"s", "es", "ing", "ed", "er", "ers", "tion", "sion",
		"ment", "ness", "ity", "age", "al", "ance", "ence",
	}
	for _, suffix := range commonSuffixes {
		if s == suffix {
			return true
		}
	}
	return false
}

// NormalizeResult holds the result from the LLM normalization
type NormalizeResult struct {
	Mappings map[string][]string `json:"mappings"` // canonical tag -> list of old tags
}

// NormalizePremappings holds the fuzzy pre-processing mappings
type NormalizePremappings struct {
	Mappings map[string][]string `json:"mappings"` // canonical tag -> list of old tags
}

// loadNormalizeResult loads normalize-result.json (LLM output)
func loadNormalizeResult() (*NormalizeResult, error) {
	data, err := os.ReadFile("normalize-result.json")
	if err != nil {
		return nil, fmt.Errorf("failed to read normalize-result.json: %w", err)
	}

	// The file format is an array of {canonical_tag, old_tags}
	var llmResult []struct {
		CanonicalTag string   `json:"canonical_tag"`
		OldTags      []string `json:"old_tags"`
	}

	if err := json.Unmarshal(data, &llmResult); err != nil {
		return nil, fmt.Errorf("failed to parse normalize-result.json: %w", err)
	}

	// Convert to map format
	mappings := make(map[string][]string)
	for _, entry := range llmResult {
		// Filter out the canonical tag itself from old_tags (only keep actual changes)
		var oldTags []string
		for _, oldTag := range entry.OldTags {
			if oldTag != entry.CanonicalTag {
				oldTags = append(oldTags, oldTag)
			}
		}
		if len(oldTags) > 0 {
			mappings[entry.CanonicalTag] = oldTags
		}
	}

	return &NormalizeResult{Mappings: mappings}, nil
}

// loadNormalizePremappings loads normalize-premappings.json (fuzzy pre-processing)
func loadNormalizePremappings() (*NormalizePremappings, error) {
	data, err := os.ReadFile("normalize-premappings.json")
	if err != nil {
		if os.IsNotExist(err) {
			return &NormalizePremappings{Mappings: make(map[string][]string)}, nil
		}
		return nil, fmt.Errorf("failed to read normalize-premappings.json: %w", err)
	}

	// The file format is an array of {canonical_tag, old_tags}
	var preMappingsArray []struct {
		CanonicalTag string   `json:"canonical_tag"`
		OldTags      []string `json:"old_tags"`
	}

	if err := json.Unmarshal(data, &preMappingsArray); err != nil {
		return nil, fmt.Errorf("failed to parse normalize-premappings.json: %w", err)
	}

	// Convert to map format
	mappings := make(map[string][]string)
	for _, entry := range preMappingsArray {
		var oldTags []string
		for _, oldTag := range entry.OldTags {
			if oldTag != entry.CanonicalTag {
				oldTags = append(oldTags, oldTag)
			}
		}
		if len(oldTags) > 0 {
			mappings[entry.CanonicalTag] = oldTags
		}
	}

	return &NormalizePremappings{Mappings: mappings}, nil
}
