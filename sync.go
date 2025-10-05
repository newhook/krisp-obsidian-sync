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
)

//go:embed summary-template.md
var obsidianSummaryTemplate string

//go:embed daily-note-template.md
var dailyNoteTemplate string

// MeetingWithSummary combines a meeting with its summary data
type MeetingWithSummary struct {
	Meeting     *Meeting
	SummaryData *SummaryData
}

// Stage 3: Sync cached meetings and summaries to Obsidian
func runSync(ctx context.Context, obsidianVaultPath string, limit int, syncState *SyncState, resync bool, testMode bool, meetingID string, cache *Cache) error {
	fmt.Println("\n=== Stage 3: Syncing to Obsidian ===")

	// Handle single meeting mode
	if meetingID != "" {
		fmt.Printf("🎯 Single meeting mode: %s\n", meetingID)
		if resync {
			fmt.Println("🔄 Forcing re-sync of this meeting")
			delete(syncState.ObsidianSyncedMeetings, meetingID)
		}
		// Process only this meeting
		return syncSingleMeeting(ctx, meetingID, obsidianVaultPath, syncState, cache)
	}

	return runSyncInternal(ctx, obsidianVaultPath, limit, syncState, resync, testMode, meetingID, cache)
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// uniqueStrings removes duplicates from a string slice
func uniqueStrings(slice []string) []string {
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

// syncSingleMeeting syncs a single meeting by ID to Obsidian
func syncSingleMeeting(ctx context.Context, meetingID string, obsidianVaultPath string, syncState *SyncState, cache *Cache) error {
	// Temporarily add meeting to synced list if not there
	if !syncState.SyncedMeetings[meetingID] {
		return fmt.Errorf("meeting %s not found in sync state (run download first)", meetingID)
	}

	// Temporarily create a new sync state with just this meeting
	tempState := &SyncState{
		path:                   syncState.path,
		SyncedMeetings:         map[string]bool{meetingID: true},
		SummarizedMeetings:     syncState.SummarizedMeetings,
		ObsidianSyncedMeetings: make(map[string]bool), // Empty so it processes this meeting
		LastSyncTime:           syncState.LastSyncTime,
	}

	// Run the sync with limit 1 and test mode true to force overwrite
	if err := runSyncInternal(ctx, obsidianVaultPath, 1, tempState, false, true, "", cache); err != nil {
		return err
	}

	// Update the real sync state (we do this manually since test mode doesn't update state)
	syncState.ObsidianSyncedMeetings[meetingID] = true
	if err := syncState.Save(); err != nil {
		return fmt.Errorf("failed to save sync state: %w", err)
	}

	return nil
}

// runSyncInternal is the internal sync logic extracted for reuse
func runSyncInternal(ctx context.Context, obsidianVaultPath string, limit int, syncState *SyncState, resync bool, testMode bool, meetingID string, cache *Cache) error {
	if testMode {
		fmt.Println("🧪 Test mode: will overwrite files without updating state")
	}

	// Load tags dictionary if it exists (for applying mappings)
	tagsDict, err := loadTagsDictionary()
	var tagMappings map[string]string // Reverse lookup: old tag -> canonical tag
	if err != nil {
		fmt.Printf("⚠ Warning: Could not load tags dictionary: %v\n", err)
		fmt.Println("  Tags will be written as-is without normalization")
	} else if tagsDict != nil {
		fmt.Printf("📚 Loaded tags dictionary with %d canonical tags\n", len(tagsDict.CanonicalTags))
		// Build reverse lookup map
		tagMappings = make(map[string]string)
		for canonical, oldTags := range tagsDict.Mappings {
			for _, oldTag := range oldTags {
				tagMappings[oldTag] = canonical
			}
		}
	}

	// If resync flag is set, clear the Obsidian sync state
	if resync && !testMode {
		fmt.Println("🔄 Resync mode: clearing Obsidian sync state")
		syncState.ObsidianSyncedMeetings = make(map[string]bool)
	}

	// Get list of meetings that need to be synced to Obsidian and load them
	var toSync []*MeetingWithSummary
	for id := range syncState.SyncedMeetings {
		// In test mode, include all meetings; otherwise only unsynced ones
		if testMode || !syncState.ObsidianSyncedMeetings[id] {
			// Load the meeting once
			meeting, err := cache.LoadMeeting(id)
			if err != nil {
				fmt.Printf("⚠ Error loading meeting %s: %v\n", id, err)
				continue
			}

			// Load summary data (if exists)
			var summaryData *SummaryData
			if cache.SummaryExists(meeting.ID) {
				summaryData, err = cache.LoadSummary(meeting.ID)
				if err != nil {
					fmt.Printf("⚠ Error loading summary for %s: %v\n", meeting.ID, err)
				}
			}

			toSync = append(toSync, &MeetingWithSummary{
				Meeting:     meeting,
				SummaryData: summaryData,
			})
		}
	}

	if len(toSync) == 0 {
		fmt.Println("✅ All downloaded meetings already synced to Obsidian!")
		return nil
	}

	// Sort by creation time (oldest first)
	sort.Slice(toSync, func(i, j int) bool {
		return toSync[i].Meeting.CreatedAt.Before(toSync[j].Meeting.CreatedAt)
	})

	// In test mode, only take the first meeting
	if testMode && len(toSync) > 0 {
		toSync = toSync[:1]
		limit = 1
		fmt.Printf("🧪 Test mode: processing first meeting only\n")
	}

	fmt.Printf("Found %d meeting(s) to sync to Obsidian (oldest to newest)\n", len(toSync))

	// Group meetings by date
	meetingsByDate := make(map[string][]*MeetingWithSummary)

	processedCount := 0
	for _, mws := range toSync {
		// Check if context was cancelled
		if ctx.Err() != nil {
			fmt.Printf("\n⚠ Sync cancelled\n")
			return ctx.Err()
		}

		if limit > 0 && processedCount >= limit {
			break
		}

		// Group by date
		dateKey := mws.Meeting.CreatedAt.Format("2006-01-02")
		meetingsByDate[dateKey] = append(meetingsByDate[dateKey], mws)

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

		// Create individual meeting files
		for _, mws := range dayMeetings {
			// Check if context was cancelled
			if ctx.Err() != nil {
				fmt.Printf("\n⚠ Sync cancelled\n")
				return ctx.Err()
			}

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
			description := ""
			var tags []string
			summary := ""
			if mws.SummaryData != nil {
				description = mws.SummaryData.Description
				// Split comma-separated tags into array and apply mappings
				if mws.SummaryData.Tags != "" {
					for _, tag := range strings.Split(mws.SummaryData.Tags, ",") {
						tag = strings.TrimSpace(tag)
						// Apply mapping if dictionary exists
						if tagMappings != nil {
							if canonical, ok := tagMappings[tag]; ok {
								tag = canonical
							}
						}
						tags = append(tags, tag)
					}
					// Remove duplicates after mapping
					tags = uniqueStrings(tags)
					sort.Strings(tags)
				}
				summary = mws.SummaryData.Summary
			}

			templateData := map[string]interface{}{
				"Date":         m.CreatedAt.Format("2006-01-02"),
				"Time":         m.CreatedAt.Format("15:04"),
				"Title":        m.Title,
				"Description":  description,
				"Tags":         tags,
				"Participants": participantsStr,
				"MeetingID":    m.ID,
				"Summary":      summary,
			}

			// Render summary file
			var summaryBuf bytes.Buffer
			if err := tmpl.Execute(&summaryBuf, templateData); err != nil {
				fmt.Printf("  ⚠ Error rendering template for %s: %v\n", m.ID, err)
				continue
			}

			// Write summary file (skip if exists unless in test mode)
			summaryFileName := fmt.Sprintf("%s-summary.md", m.ID)
			summaryFilePath := filepath.Join(meetingsPath, summaryFileName)
			if !testMode && fileExists(summaryFilePath) {
				fmt.Printf("  ⏭  Summary exists, skipping: %s\n", summaryFileName)
			} else {
				if err := os.WriteFile(summaryFilePath, summaryBuf.Bytes(), 0644); err != nil {
					fmt.Printf("  ⚠ Error writing summary file: %v\n", err)
					continue
				}
				if testMode {
					fmt.Printf("  ✓ Overwrote summary: %s\n", summaryFileName)
				} else {
					fmt.Printf("  ✓ Created summary: %s\n", summaryFileName)
				}
			}

			// Generate transcript file (skip if exists unless in test mode)
			transcriptFileName := fmt.Sprintf("%s-transcript.md", m.ID)
			transcriptFilePath := filepath.Join(meetingsPath, transcriptFileName)
			if !testMode && fileExists(transcriptFilePath) {
				fmt.Printf("  ⏭  Transcript exists, skipping: %s\n", transcriptFileName)
			} else {
				transcriptContent := generateTranscriptContent(m)
				if err := os.WriteFile(transcriptFilePath, []byte(transcriptContent), 0644); err != nil {
					fmt.Printf("  ⚠ Error writing transcript file: %v\n", err)
					continue
				}
				if testMode {
					fmt.Printf("  ✓ Overwrote transcript: %s\n", transcriptFileName)
				} else {
					fmt.Printf("  ✓ Created transcript: %s\n", transcriptFileName)
				}
			}

			// Mark meeting as synced to Obsidian (skip in test mode)
			if !testMode {
				syncState.ObsidianSyncedMeetings[m.ID] = true

				// Save state after each meeting sync
				if err := syncState.Save(); err != nil {
					fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
				}
			}

			successCount++
		}

		// Create or update daily note with Dataview query
		filename := fmt.Sprintf("%s-%s.md", date, dayName)
		filePath := filepath.Join(dailyNotesPath, filename)

		// In test mode, always overwrite; otherwise skip if exists
		if !testMode && fileExists(filePath) {
			fmt.Printf("  ✓ Daily note already exists: %s (using Dataview query)\n", filename)
		} else {
			// Create daily note with Dataview query
			dailyNoteTmpl, err := template.New("dailynote").Parse(dailyNoteTemplate)
			if err != nil {
				fmt.Printf("  ⚠ Error parsing daily note template: %v\n", err)
				continue
			}

			dailyNoteData := map[string]string{
				"Date":      date,
				"YearPath":  year,
				"MonthPath": monthNum + "-" + monthName,
			}

			var dailyNoteBuf bytes.Buffer
			if err := dailyNoteTmpl.Execute(&dailyNoteBuf, dailyNoteData); err != nil {
				fmt.Printf("  ⚠ Error rendering daily note template: %v\n", err)
				continue
			}

			if err := os.WriteFile(filePath, dailyNoteBuf.Bytes(), 0644); err != nil {
				fmt.Printf("  ⚠ Error writing daily note: %v\n", err)
				continue
			}

			if testMode {
				fmt.Printf("  ✓ Overwrote daily note: %s (with Dataview query)\n", filename)
			} else {
				fmt.Printf("  ✓ Created daily note: %s (with Dataview query)\n", filename)
			}
		}

		fmt.Printf("  ✓ Synced %d meeting file(s)\n", len(dayMeetings))
	}

	fmt.Printf("\n✅ Synced %d meeting(s) to %d daily note(s)\n", successCount, len(meetingsByDate))
	return nil
}
