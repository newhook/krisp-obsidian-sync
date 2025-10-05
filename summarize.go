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
func runSummarize(ctx context.Context, limit int, syncState *SyncState, resummarize bool, meetingID string, cache *Cache) error {
	fmt.Println("\n=== Stage 2: Summarizing meetings ===")

	// Handle single meeting mode
	if meetingID != "" {
		fmt.Printf("🎯 Single meeting mode: %s\n", meetingID)
		if resummarize {
			fmt.Println("🔄 Forcing re-summarization of this meeting")
			delete(syncState.SummarizedMeetings, meetingID)
		}
		// Process only this meeting
		return summarizeSingleMeeting(ctx, meetingID, syncState, cache)
	}

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

	// Load all meetings first (cache is not thread-safe)
	type meetingWithTranscript struct {
		ID         string
		Transcript string
	}
	var meetingsToProcess []meetingWithTranscript

	for _, m := range toSummarize {
		meeting, err := cache.LoadMeeting(m.ID)
		if err != nil {
			fmt.Printf("⚠ Error loading meeting %s: %v\n", m.ID, err)
			continue
		}

		// Parse transcript
		var transcriptText string
		if meeting.Resources.Transcript.Status != "uploaded" {
			fmt.Printf("⚠ Transcript not uploaded for %s (status: %s)\n", m.ID, meeting.Resources.Transcript.Status)
			continue
		}
		if meeting.Resources.Transcript.Content == "" {
			fmt.Printf("⚠ Transcript content empty for %s\n", m.ID)
			continue
		}

		var segments []Segment
		if err := json.Unmarshal([]byte(meeting.Resources.Transcript.Content), &segments); err != nil {
			fmt.Printf("⚠ Error parsing transcript JSON for %s: %v\n", m.ID, err)
			continue
		}

		if len(segments) == 0 {
			fmt.Printf("⚠ Transcript has no segments for %s\n", m.ID)
			continue
		}

		var sb strings.Builder
		for _, seg := range segments {
			// Get speaker name from the speakers map
			speakerName := fmt.Sprintf("Speaker %d", seg.SpeakerIndex)
			if speakerInfo, ok := meeting.Speakers.Data[fmt.Sprintf("%d", seg.SpeakerIndex)]; ok {
				speakerName = strings.TrimSpace(speakerInfo.Person.FirstName + " " + speakerInfo.Person.LastName)
				if speakerName == "" {
					speakerName = fmt.Sprintf("Speaker %d", seg.SpeakerIndex)
				}
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", speakerName, seg.Speech.Text))
		}
		transcriptText = sb.String()

		if transcriptText == "" {
			fmt.Printf("⚠ Generated transcript text is empty for %s\n", m.ID)
			continue
		}

		meetingsToProcess = append(meetingsToProcess, meetingWithTranscript{
			ID:         m.ID,
			Transcript: transcriptText,
		})
	}

	if len(meetingsToProcess) == 0 {
		fmt.Println("⚠ No meetings with transcripts to process")
		return nil
	}

	// Process summaries in parallel with concurrency limit
	const maxConcurrency = 10
	semaphore := make(chan struct{}, maxConcurrency)

	type result struct {
		index int
		id    string
		data  *SummaryData
		err   error
	}
	results := make(chan result, len(meetingsToProcess))

	// Process each meeting in parallel
	for i, m := range meetingsToProcess {
		// Check if context was cancelled
		if ctx.Err() != nil {
			fmt.Printf("\n⚠ Summarization cancelled\n")
			return ctx.Err()
		}

		semaphore <- struct{}{} // Acquire semaphore

		go func(index int, meetingID string, transcript string) {
			defer func() { <-semaphore }() // Release semaphore

			fmt.Printf("[%d/%d] Summarizing meeting: %s\n", index+1, len(meetingsToProcess), meetingID)

			// Generate summary with Gemini
			summaryResponse, err := summarizeWithGemini(ctx, transcript, existingTags)
			if err != nil {
				fmt.Printf("  ⚠ Error generating summary: %v\n", err)
				results <- result{index: index, id: meetingID, err: err}
				return
			}

			// Parse the summary response to SummaryData
			summaryData := parseSummaryResponse(summaryResponse)

			fmt.Printf("  ✓ Summary generated: %s\n", meetingID)
			results <- result{index: index, id: meetingID, data: summaryData, err: nil}
		}(i, m.ID, m.Transcript)
	}

	// Wait for all goroutines to complete and save results
	successCount := 0
	for i := 0; i < len(meetingsToProcess); i++ {
		res := <-results
		if res.err == nil {
			// Save summary to cache
			if err := cache.SaveSummary(res.id, res.data); err != nil {
				fmt.Printf("  ⚠ Error saving summary for %s: %v\n", res.id, err)
				continue
			}
			fmt.Printf("  ✓ Summary saved: meetings/%s-summary.json\n", res.id)

			syncState.SummarizedMeetings[res.id] = true
			successCount++
			// Save state after each successful summary
			if err := syncState.Save(); err != nil {
				fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
			}
		}
	}

	fmt.Printf("\n✅ Summarized %d meeting(s)\n", successCount)
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

// summarizeSingleMeeting summarizes a single meeting by ID
func summarizeSingleMeeting(ctx context.Context, meetingID string, syncState *SyncState, cache *Cache) error {
	// Load tags dictionary if it exists
	dict, err := loadTagsDictionary()
	var existingTags []string
	if err == nil && dict != nil {
		existingTags = dict.CanonicalTags
		fmt.Printf("📚 Loaded %d canonical tags from dictionary\n", len(existingTags))
	}

	// Load the meeting
	meeting, err := cache.LoadMeeting(meetingID)
	if err != nil {
		return fmt.Errorf("failed to load meeting %s: %w", meetingID, err)
	}

	// Parse transcript
	if meeting.Resources.Transcript.Status != "uploaded" {
		return fmt.Errorf("transcript not uploaded for %s (status: %s)", meetingID, meeting.Resources.Transcript.Status)
	}
	if meeting.Resources.Transcript.Content == "" {
		return fmt.Errorf("transcript content empty for %s", meetingID)
	}

	var segments []Segment
	if err := json.Unmarshal([]byte(meeting.Resources.Transcript.Content), &segments); err != nil {
		return fmt.Errorf("error parsing transcript JSON for %s: %w", meetingID, err)
	}

	if len(segments) == 0 {
		return fmt.Errorf("transcript has no segments for %s", meetingID)
	}

	// Build transcript with speaker names
	var sb strings.Builder
	for _, seg := range segments {
		speakerName := fmt.Sprintf("Speaker %d", seg.SpeakerIndex)
		if speakerInfo, ok := meeting.Speakers.Data[fmt.Sprintf("%d", seg.SpeakerIndex)]; ok {
			speakerName = strings.TrimSpace(speakerInfo.Person.FirstName + " " + speakerInfo.Person.LastName)
			if speakerName == "" {
				speakerName = fmt.Sprintf("Speaker %d", seg.SpeakerIndex)
			}
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", speakerName, seg.Speech.Text))
	}
	transcriptText := sb.String()

	fmt.Printf("Summarizing meeting: %s\n", meetingID)

	// Generate summary with Gemini
	summaryResponse, err := summarizeWithGemini(ctx, transcriptText, existingTags)
	if err != nil {
		return fmt.Errorf("error generating summary: %w", err)
	}

	// Parse the summary response to SummaryData
	summaryData := parseSummaryResponse(summaryResponse)

	// Save summary to cache
	if err := cache.SaveSummary(meetingID, summaryData); err != nil {
		return fmt.Errorf("error saving summary: %w", err)
	}

	// Update sync state
	syncState.SummarizedMeetings[meetingID] = true
	if err := syncState.Save(); err != nil {
		fmt.Printf("⚠ Warning: Could not save sync state: %v\n", err)
	}

	fmt.Printf("✅ Successfully summarized meeting: %s\n", meetingID)
	return nil
}
