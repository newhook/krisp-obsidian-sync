package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// Stage 3: Sync cached meetings and summaries to Obsidian
func runSync(obsidianVaultPath string, limit int) error {
	fmt.Println("\n=== Stage 3: Syncing to Obsidian ===")

	// Get all cached meeting files
	files, err := filepath.Glob(filepath.Join(meetingsCacheDir, "*.json"))
	if err != nil {
		return fmt.Errorf("error reading cache directory: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("⚠ No cached meetings found. Run download step first.")
		return nil
	}

	// Load all meetings and group by date
	type MeetingWithSummary struct {
		Meeting *Meeting
		Summary string
	}

	meetingsByDate := make(map[string][]*MeetingWithSummary)

	processedCount := 0
	for _, file := range files {
		if limit > 0 && processedCount >= limit {
			break
		}

		meetingID := strings.TrimSuffix(filepath.Base(file), ".json")

		// Load meeting from cache
		meeting, err := loadMeetingFromCache(meetingID)
		if err != nil {
			fmt.Printf("⚠ Error loading meeting %s: %v\n", meetingID, err)
			continue
		}

		// Load summary from cache (if exists)
		summary := ""
		if summaryExistsInCache(meetingID) {
			summary, err = loadSummaryFromCache(meetingID)
			if err != nil {
				fmt.Printf("⚠ Error loading summary for %s: %v\n", meetingID, err)
			}
		}

		dateKey := meeting.CreatedAt.Format("2006-01-02")
		meetingsByDate[dateKey] = append(meetingsByDate[dateKey], &MeetingWithSummary{
			Meeting: meeting,
			Summary: summary,
		})

		processedCount++
	}

	// Parse the summary template
	tmpl, err := template.New("summary").Parse(obsidianSummaryTemplate)
	if err != nil {
		return fmt.Errorf("error parsing template: %w", err)
	}

	// Process each day
	successCount := 0
	for date, dayMeetings := range meetingsByDate {
		fmt.Printf("\n📅 Processing %s (%d meeting(s))\n", date, len(dayMeetings))

		// Sort meetings by time
		sort.Slice(dayMeetings, func(i, j int) bool {
			return dayMeetings[i].Meeting.CreatedAt.Before(dayMeetings[j].Meeting.CreatedAt)
		})

		// Generate path: YYYY/MM-MonthName/YYYY-MM-DD-DayName.md
		t := dayMeetings[0].Meeting.CreatedAt
		year := t.Format("2006")
		monthNum := t.Format("01")
		monthName := t.Format("January")
		dayName := t.Format("Monday")

		// Create directory structure: YYYY/MM-MonthName
		dailyNotesPath := filepath.Join(obsidianVaultPath, year, monthNum+"-"+monthName)
		if err := os.MkdirAll(dailyNotesPath, 0755); err != nil {
			fmt.Printf("  ⚠ Error creating directory: %v\n", err)
			continue
		}

		// Create meetings subdirectory
		meetingsPath := filepath.Join(dailyNotesPath, "meetings")
		if err := os.MkdirAll(meetingsPath, 0755); err != nil {
			fmt.Printf("  ⚠ Error creating meetings directory: %v\n", err)
			continue
		}

		// Create individual meeting files and collect links
		var meetingLinks []string
		for _, mws := range dayMeetings {
			m := mws.Meeting

			// Get participants from speakers
			var participants []string
			for _, speakerInfo := range m.Speakers.Data {
				name := strings.TrimSpace(speakerInfo.Person.FirstName + " " + speakerInfo.Person.LastName)
				if name != "" {
					participants = append(participants, name)
				}
			}
			participantsStr := strings.Join(participants, ", ")
			if participantsStr == "" {
				participantsStr = "[]"
			}

			// Prepare template data for summary file
			templateData := map[string]string{
				"Date":         m.CreatedAt.Format("2006-01-02"),
				"Time":         m.CreatedAt.Format("15:04"),
				"Title":        m.Title,
				"Tags":         "[]",
				"Participants": participantsStr,
				"MeetingID":    m.ID,
				"Summary":      mws.Summary,
			}

			// Render summary file
			var summaryBuf bytes.Buffer
			if err := tmpl.Execute(&summaryBuf, templateData); err != nil {
				fmt.Printf("  ⚠ Error rendering template for %s: %v\n", m.ID, err)
				continue
			}

			// Write summary file
			summaryFileName := fmt.Sprintf("%s-summary.md", m.ID)
			summaryFilePath := filepath.Join(meetingsPath, summaryFileName)
			if err := os.WriteFile(summaryFilePath, summaryBuf.Bytes(), 0644); err != nil {
				fmt.Printf("  ⚠ Error writing summary file: %v\n", err)
				continue
			}

			// Generate transcript file
			transcriptFileName := fmt.Sprintf("%s-transcript.md", m.ID)
			transcriptFilePath := filepath.Join(meetingsPath, transcriptFileName)
			transcriptContent := generateTranscriptContent(m)
			if err := os.WriteFile(transcriptFilePath, []byte(transcriptContent), 0644); err != nil {
				fmt.Printf("  ⚠ Error writing transcript file: %v\n", err)
				continue
			}

			// Add link to daily note (only link to summary)
			meetingLinks = append(meetingLinks, fmt.Sprintf("- [[meetings/%s|%s - %s]]",
				m.ID+"-summary",
				m.CreatedAt.Format("3:04 PM"),
				m.Title))
		}

		// Update daily note with meeting links
		filename := fmt.Sprintf("%s-%s.md", date, dayName)
		filePath := filepath.Join(dailyNotesPath, filename)

		// Check if daily note already exists
		existingContent := ""
		if data, err := os.ReadFile(filePath); err == nil {
			existingContent = string(data)
		}

		// Generate content with meeting links
		meetingsContent := strings.Join(meetingLinks, "\n") + "\n\n"

		// Append or create the daily note
		var finalContent string
		if existingContent != "" {
			// Append to existing note
			finalContent = appendToExistingNote(existingContent, meetingsContent)
		} else {
			// Create new daily note
			finalContent = createNewDailyNote(date, meetingsContent)
		}

		if err := os.WriteFile(filePath, []byte(finalContent), 0644); err != nil {
			fmt.Printf("  ⚠ Error writing file: %v\n", err)
			continue
		}

		fmt.Printf("  ✓ Updated: %s (%d meeting file(s))\n", filename, len(dayMeetings))
		successCount += len(dayMeetings)
	}

	fmt.Printf("\n✅ Synced %d meeting(s) to %d daily note(s)\n", successCount, len(meetingsByDate))
	return nil
}

func generateTranscriptContent(m *Meeting) string {
	var sb strings.Builder

	// Transcript header
	timeStr := m.CreatedAt.Format("3:04 PM")
	dateStr := m.CreatedAt.Format("Monday, January 2, 2006")
	sb.WriteString(fmt.Sprintf("# %s - %s (Transcript)\n\n", timeStr, m.Title))
	sb.WriteString(fmt.Sprintf("**Date**: %s\n", dateStr))
	sb.WriteString(fmt.Sprintf("**Meeting ID**: `%s`\n\n", m.ID))

	// Full transcript
	if m.Resources.Transcript.Status == "uploaded" && m.Resources.Transcript.Content != "" {
		var segments []Segment
		if err := json.Unmarshal([]byte(m.Resources.Transcript.Content), &segments); err == nil && len(segments) > 0 {
			sb.WriteString("## Transcript\n\n")

			for _, segment := range segments {
				timestamp := formatTimestamp(segment.Speech.Start)

				// Get speaker name from the speakers map
				speakerName := fmt.Sprintf("Speaker %d", segment.SpeakerIndex)
				if speakerInfo, ok := m.Speakers.Data[fmt.Sprintf("%d", segment.SpeakerIndex)]; ok {
					if speakerInfo.Person.FirstName != "" || speakerInfo.Person.LastName != "" {
						speakerName = strings.TrimSpace(speakerInfo.Person.FirstName + " " + speakerInfo.Person.LastName)
					}
				}

				sb.WriteString(fmt.Sprintf("**[%s] %s**: %s\n\n", timestamp, speakerName, segment.Speech.Text))
			}
		}
	}

	return sb.String()
}

func createNewDailyNote(date string, meetingsContent string) string {
	var sb strings.Builder

	// Basic daily note structure
	sb.WriteString("# " + date + "\n\n")
	sb.WriteString("## Meetings\n\n")
	sb.WriteString(meetingsContent)

	return sb.String()
}

func appendToExistingNote(existingContent, meetingsContent string) string {
	// Check if there's already a Meetings section
	if strings.Contains(existingContent, "## Meetings") {
		// Find the Meetings section and append there
		lines := strings.Split(existingContent, "\n")
		var result strings.Builder
		inMeetingsSection := false
		meetingsAdded := false

		for i, line := range lines {
			result.WriteString(line + "\n")

			if strings.HasPrefix(line, "## Meetings") {
				inMeetingsSection = true
			} else if inMeetingsSection && strings.HasPrefix(line, "## ") {
				// Found next section, insert meetings before it
				if !meetingsAdded {
					result.WriteString(meetingsContent)
					meetingsAdded = true
				}
				inMeetingsSection = false
			}

			// If we're at the end and still in meetings section
			if inMeetingsSection && i == len(lines)-1 && !meetingsAdded {
				result.WriteString(meetingsContent)
				meetingsAdded = true
			}
		}

		// If meetings section was at the end
		if inMeetingsSection && !meetingsAdded {
			result.WriteString(meetingsContent)
		}

		return result.String()
	} else {
		// No Meetings section exists, append it at the end
		return existingContent + "\n## Meetings\n\n" + meetingsContent
	}
}

// Legacy function - kept for reference but not currently used
func generateMeetingContent(m *Meeting) string {
	var sb strings.Builder

	// Meeting header
	timeStr := m.CreatedAt.Format("3:04 PM")
	dateStr := m.CreatedAt.Format("Monday, January 2, 2006")
	sb.WriteString(fmt.Sprintf("# %s - %s\n\n", timeStr, m.Title))
	sb.WriteString(fmt.Sprintf("**Date**: %s\n", dateStr))

	// Metadata
	if m.Duration > 0 {
		minutes := m.Duration / 60
		seconds := m.Duration % 60
		sb.WriteString(fmt.Sprintf("**Duration**: %d:%02d\n", minutes, seconds))
	}
	sb.WriteString(fmt.Sprintf("**Meeting ID**: `%s`\n\n", m.ID))

	// Summary
	if m.Summary != "" {
		sb.WriteString("## Summary\n\n")
		sb.WriteString(m.Summary + "\n\n")
	}

	// Notes
	if m.Notes != "" {
		sb.WriteString("## Notes\n\n")
		sb.WriteString(m.Notes + "\n\n")
	}

	// Transcript in collapsible section
	if m.Resources.Transcript.Status == "uploaded" && m.Resources.Transcript.Content != "" {
		var segments []Segment
		if err := json.Unmarshal([]byte(m.Resources.Transcript.Content), &segments); err == nil && len(segments) > 0 {
			sb.WriteString("## Transcript\n\n")
			sb.WriteString("<details>\n<summary>📝 Full Transcript</summary>\n\n")

			for _, segment := range segments {
				timestamp := formatTimestamp(segment.Speech.Start)

				// Get speaker name from the speakers map
				speakerName := fmt.Sprintf("Speaker %d", segment.SpeakerIndex)
				if speakerInfo, ok := m.Speakers.Data[fmt.Sprintf("%d", segment.SpeakerIndex)]; ok {
					if speakerInfo.Person.FirstName != "" || speakerInfo.Person.LastName != "" {
						speakerName = strings.TrimSpace(speakerInfo.Person.FirstName + " " + speakerInfo.Person.LastName)
					}
				}

				sb.WriteString(fmt.Sprintf("**[%s] %s**: %s\n\n", timestamp, speakerName, segment.Speech.Text))
			}

			sb.WriteString("</details>\n\n")
		}
	}

	return sb.String()
}
