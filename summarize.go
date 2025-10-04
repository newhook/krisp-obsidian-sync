package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"google.golang.org/genai"
)

// Stage 2: Summarize cached meetings with Gemini
func runSummarize(limit int, syncState *SyncState, syncStatePath string, ctx context.Context) error {
	fmt.Println("\n=== Stage 2: Summarizing meetings ===")

	// Get all cached meeting files
	files, err := filepath.Glob(filepath.Join(meetingsCacheDir, "*.json"))
	if err != nil {
		return fmt.Errorf("error reading cache directory: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("⚠ No cached meetings found. Run download step first.")
		return nil
	}

	// Filter to meetings that need summarization
	var toSummarize []string
	for _, file := range files {
		meetingID := strings.TrimSuffix(filepath.Base(file), ".json")
		if !summaryExistsInCache(meetingID) {
			toSummarize = append(toSummarize, meetingID)
		}
	}

	if len(toSummarize) == 0 {
		fmt.Println("✅ All cached meetings already summarized!")
		return nil
	}

	fmt.Printf("Found %d meeting(s) to summarize\n", len(toSummarize))

	// Apply limit
	if limit > 0 && len(toSummarize) > limit {
		fmt.Printf("⚠ Limiting to %d meeting(s) for this run\n", limit)
		toSummarize = toSummarize[:limit]
	}

	// Summarize each meeting
	for i, meetingID := range toSummarize {
		fmt.Printf("[%d/%d] Summarizing meeting: %s\n", i+1, len(toSummarize), meetingID)

		// Load from cache
		meeting, err := loadMeetingFromCache(meetingID)
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
		summary, err := summarizeWithGemini(ctx, transcriptText)
		if err != nil {
			fmt.Printf("  ⚠ Error generating summary: %v\n", err)
			continue
		}

		// Save summary to cache
		if err := saveSummaryToCache(meetingID, summary); err != nil {
			fmt.Printf("  ⚠ Error saving summary: %v\n", err)
			continue
		}

		syncState.SummarizedMeetings[meetingID] = true
		fmt.Printf("  ✓ Summary saved: meetings/%s-summary.md\n", meetingID)

		// Save state after each summary
		if err := saveSyncState(syncStatePath, syncState); err != nil {
			fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
		}

		// Be nice to the API
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Printf("\n✅ Summarized %d meeting(s)\n", len(toSummarize))
	return nil
}

func summarizeWithGemini(ctx context.Context, transcript string) (string, error) {
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

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash-lite", []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				genai.NewPartFromText(prompt),
			},
		},
	}, &genai.GenerateContentConfig{
		Temperature:     func() *float32 { v := float32(0.3); return &v }(),
		MaxOutputTokens: 2048,
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
