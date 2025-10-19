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

	"gopkg.in/yaml.v3"
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
func runSync(ctx context.Context, obsidianVaultPath string, limit int, syncState *SyncState, overwrite bool, testMode bool, applyNormalization bool, meetingIDs []string, updateFields []string, cache *Cache) error {
	fmt.Println("\n=== Stage 3: Syncing to Obsidian ===")

	// Handle specific meeting IDs mode
	if len(meetingIDs) > 0 {
		fmt.Printf("🎯 Processing %d specific meeting(s)\n", len(meetingIDs))
		if overwrite {
			fmt.Println("🔄 Forcing re-sync of specified meetings")
			for _, id := range meetingIDs {
				delete(syncState.ObsidianSyncedMeetings, id)
			}
		}
		// Process each meeting
		for _, meetingID := range meetingIDs {
			if err := syncSingleMeeting(ctx, meetingID, obsidianVaultPath, syncState, applyNormalization, updateFields, cache); err != nil {
				fmt.Printf("❌ Error syncing meeting %s: %v\n", meetingID, err)
				// Continue with other meetings
			}
		}
		return nil
	}

	return runSyncInternal(ctx, obsidianVaultPath, limit, syncState, overwrite, testMode, applyNormalization, updateFields, cache)
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// parseFrontmatter extracts YAML frontmatter and body from a markdown file
func parseFrontmatter(filePath string) (map[string]interface{}, string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, "", err
	}

	// Check for frontmatter delimiters
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return nil, "", fmt.Errorf("file does not have YAML frontmatter")
	}

	// Find the end of frontmatter
	parts := bytes.SplitN(content[4:], []byte("\n---\n"), 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("malformed YAML frontmatter")
	}

	// Parse YAML
	var frontmatter map[string]interface{}
	if err := yaml.Unmarshal(parts[0], &frontmatter); err != nil {
		return nil, "", fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	body := string(parts[1])
	return frontmatter, body, nil
}

// updateFrontmatterFields updates specific fields in existing frontmatter
func updateFrontmatterFields(existingFrontmatter map[string]interface{}, newData map[string]interface{}, fieldsToUpdate []string) map[string]interface{} {
	updated := make(map[string]interface{})

	// Copy existing frontmatter
	for k, v := range existingFrontmatter {
		updated[k] = v
	}

	// Update only specified fields (case-insensitive match)
	for _, field := range fieldsToUpdate {
		fieldLower := strings.ToLower(field)
		// Look for the field in newData with case-insensitive matching
		for key, value := range newData {
			if strings.ToLower(key) == fieldLower {
				// Update using the lowercase field name (matches frontmatter convention)
				updated[field] = value
				break
			}
		}
	}

	return updated
}

// writeFrontmatterFile writes a markdown file with YAML frontmatter
func writeFrontmatterFile(filePath string, frontmatter map[string]interface{}, body string) error {
	var buf bytes.Buffer

	buf.WriteString("---\n")

	// Write frontmatter fields in a consistent order
	orderedKeys := []string{"date", "time", "type", "title", "description", "tags", "participants", "meeting_id"}
	for _, key := range orderedKeys {
		if value, ok := frontmatter[key]; ok {
			writeFrontmatterField(&buf, key, value)
		}
	}

	// Write any remaining fields not in the ordered list
	for key, value := range frontmatter {
		if !contains(orderedKeys, key) {
			writeFrontmatterField(&buf, key, value)
		}
	}

	buf.WriteString("---\n")
	buf.WriteString(body)

	return os.WriteFile(filePath, buf.Bytes(), 0644)
}

// writeFrontmatterField writes a single frontmatter field
func writeFrontmatterField(buf *bytes.Buffer, key string, value interface{}) {
	switch v := value.(type) {
	case []interface{}:
		// Array field (like tags)
		buf.WriteString(key + ":\n")
		for _, item := range v {
			buf.WriteString(fmt.Sprintf("  - \"%v\"\n", item))
		}
	case []string:
		// String array field
		buf.WriteString(key + ":\n")
		for _, item := range v {
			buf.WriteString(fmt.Sprintf("  - \"%v\"\n", item))
		}
	case string:
		// String field - quote if it contains YAML special characters
		if needsQuoting(v) {
			buf.WriteString(fmt.Sprintf("%s: \"%s\"\n", key, v))
		} else {
			buf.WriteString(fmt.Sprintf("%s: %s\n", key, v))
		}
	case time.Time:
		// Time field - format as YYYY-MM-DD for date fields
		if key == "date" {
			buf.WriteString(fmt.Sprintf("%s: %s\n", key, v.Format("2006-01-02")))
		} else {
			buf.WriteString(fmt.Sprintf("%s: %s\n", key, v.Format(time.RFC3339)))
		}
	default:
		// Other types - convert to string representation
		strValue := fmt.Sprintf("%v", v)
		buf.WriteString(fmt.Sprintf("%s: %s\n", key, strValue))
	}
}

// needsQuoting checks if a string value needs to be quoted in YAML
func needsQuoting(s string) bool {
	// Quote if string contains: colon, quotes, brackets, braces, or other YAML special chars
	specialChars := []string{":", "\"", "'", "[", "]", "{", "}", "#", "&", "*", "!", "|", ">", "%", "@"}
	for _, char := range specialChars {
		if strings.Contains(s, char) {
			return true
		}
	}
	return false
}

// contains checks if a string slice contains a value
func contains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
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

// updateDailyNoteDataview updates the Dataview query in an existing daily note
func updateDailyNoteDataview(filePath string, data map[string]string) error {
	// Read existing daily note
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// Generate new Dataview query from template
	dailyNoteTmpl, err := template.New("dailynote").Parse(dailyNoteTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	var dailyNoteBuf bytes.Buffer
	if err := dailyNoteTmpl.Execute(&dailyNoteBuf, data); err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	newContent := dailyNoteBuf.String()

	// Extract the new Dataview query (between ```dataview and ```)
	newDataviewStart := strings.Index(newContent, "```dataview")
	newDataviewEnd := strings.Index(newContent[newDataviewStart:], "```\n")
	if newDataviewStart == -1 || newDataviewEnd == -1 {
		return fmt.Errorf("could not find dataview query in template")
	}
	newDataview := newContent[newDataviewStart : newDataviewStart+newDataviewEnd+4] // +4 for "```\n"

	// Find and replace the old Dataview query
	contentStr := string(content)
	oldDataviewStart := strings.Index(contentStr, "```dataview")

	if oldDataviewStart == -1 {
		// No dataview query exists, append it after "## Meetings" header
		meetingsHeaderIdx := strings.Index(contentStr, "## Meetings")
		if meetingsHeaderIdx == -1 {
			// No meetings header, append at end
			contentStr = contentStr + "\n## Meetings\n\n" + newDataview
		} else {
			// Insert after "## Meetings" header
			insertPos := meetingsHeaderIdx + len("## Meetings\n\n")
			contentStr = contentStr[:insertPos] + newDataview + "\n" + contentStr[insertPos:]
		}
	} else {
		// Replace existing dataview query
		oldDataviewEnd := strings.Index(contentStr[oldDataviewStart:], "```\n")
		if oldDataviewEnd == -1 {
			return fmt.Errorf("malformed dataview query in file")
		}
		oldDataviewEnd = oldDataviewStart + oldDataviewEnd + 4 // +4 for "```\n"

		contentStr = contentStr[:oldDataviewStart] + newDataview + contentStr[oldDataviewEnd:]
	}

	// Write updated content back
	return os.WriteFile(filePath, []byte(contentStr), 0644)
}

func generateTranscriptContent(m *Meeting) string {
	var sb strings.Builder

	// Transcript header
	timeStr := m.CreatedAt.Local().Format("3:04 PM")
	dateStr := m.CreatedAt.Local().Format("Monday, January 2, 2006")
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
func syncSingleMeeting(ctx context.Context, meetingID string, obsidianVaultPath string, syncState *SyncState, applyNormalization bool, updateFields []string, cache *Cache) error {
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
	if err := runSyncInternal(ctx, obsidianVaultPath, 1, tempState, false, true, applyNormalization, updateFields, cache); err != nil {
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
func runSyncInternal(ctx context.Context, obsidianVaultPath string, limit int, syncState *SyncState, overwrite bool, testMode bool, applyNormalization bool, updateFields []string, cache *Cache) error {
	if testMode {
		fmt.Println("🧪 Test mode: will overwrite files without updating state")
	}

	if len(updateFields) > 0 {
		fmt.Printf("📝 Selective update mode: updating only fields %v in existing files\n", updateFields)
	}

	// Load normalization mappings if requested (for initial mass import)
	var tagMappings map[string]string // Reverse lookup: old tag -> canonical tag
	if applyNormalization {
		fmt.Println("📚 Loading normalization mappings for initial mass import...")

		// Load normalize-result.json (LLM output)
		normalizeResult, err := loadNormalizeResult()
		if err != nil {
			fmt.Printf("⚠ Warning: Could not load normalize-result.json: %v\n", err)
			fmt.Println("  Tags will be written as-is without normalization")
		} else {
			// Load normalize-premappings.json (manual mappings)
			premappings, err := loadNormalizePremappings()
			if err != nil {
				fmt.Printf("⚠ Warning: Could not load normalize-premappings.json: %v\n", err)
				premappings = &NormalizePremappings{Mappings: make(map[string][]string)}
			}

			// Merge the mappings
			tagMappings = make(map[string]string)

			// Apply LLM mappings first
			for canonical, oldTags := range normalizeResult.Mappings {
				for _, oldTag := range oldTags {
					tagMappings[oldTag] = canonical
				}
			}

			// Apply premappings (override LLM if conflicts)
			for canonical, oldTags := range premappings.Mappings {
				for _, oldTag := range oldTags {
					tagMappings[oldTag] = canonical
				}
			}

			fmt.Printf("📝 Loaded %d tag mappings\n", len(tagMappings))
		}
	}

	// If overwrite flag is set, clear the Obsidian sync state
	if overwrite && !testMode {
		fmt.Println("🔄 Overwrite mode: clearing Obsidian sync state")
		syncState.ObsidianSyncedMeetings = make(map[string]bool)
	}

	// Get list of meetings that need to be synced to Obsidian and load them
	var toSync []*MeetingWithSummary
	for id := range syncState.SyncedMeetings {
		// Determine if we should process this meeting:
		// - testMode: process all meetings
		// - updateFields: process already-synced meetings (to update existing files)
		// - otherwise: only process unsynced meetings
		shouldProcess := testMode ||
			(len(updateFields) > 0 && syncState.ObsidianSyncedMeetings[id]) ||
			(!syncState.ObsidianSyncedMeetings[id])

		if shouldProcess {
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
		dateKey := mws.Meeting.CreatedAt.Local().Format("2006-01-02")
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
		t := dayMeetings[0].Meeting.CreatedAt.Local()
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
				"Date":         m.CreatedAt.Local().Format("2006-01-02"),
				"Time":         m.CreatedAt.Local().Format("15:04"),
				"Title":        m.Title,
				"Description":  description,
				"Tags":         tags,
				"Participants": participantsStr,
				"MeetingID":    m.ID,
				"Summary":      summary,
			}

			// Write summary file
			summaryFileName := fmt.Sprintf("%s-summary.md", m.ID)
			summaryFilePath := filepath.Join(meetingsPath, summaryFileName)

			// Handle selective field updates if --update-fields is specified
			if len(updateFields) > 0 && fileExists(summaryFilePath) {
				// Read existing file and update only specified fields
				existingFrontmatter, body, err := parseFrontmatter(summaryFilePath)
				if err != nil {
					fmt.Printf("  ⚠ Error parsing existing file %s: %v\n", summaryFileName, err)
					continue
				}

				// Update only specified fields
				updatedFrontmatter := updateFrontmatterFields(existingFrontmatter, templateData, updateFields)

				// Write back with updated fields
				if err := writeFrontmatterFile(summaryFilePath, updatedFrontmatter, body); err != nil {
					fmt.Printf("  ⚠ Error updating summary file: %v\n", err)
					continue
				}

				fmt.Printf("  ✓ Updated fields %v in: %s\n", updateFields, summaryFileName)
			} else {
				// Standard sync: render and write full file
				var summaryBuf bytes.Buffer
				if err := tmpl.Execute(&summaryBuf, templateData); err != nil {
					fmt.Printf("  ⚠ Error rendering template for %s: %v\n", m.ID, err)
					continue
				}

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

		dailyNoteData := map[string]string{
			"Date":      date,
			"YearPath":  year,
			"MonthPath": monthNum + "-" + monthName,
		}

		if fileExists(filePath) {
			// Update existing daily note's Dataview query
			if err := updateDailyNoteDataview(filePath, dailyNoteData); err != nil {
				fmt.Printf("  ⚠ Error updating daily note Dataview: %v\n", err)
			} else {
				fmt.Printf("  ✓ Updated daily note Dataview: %s\n", filename)
			}
		} else {
			// Create new daily note with Dataview query
			dailyNoteTmpl, err := template.New("dailynote").Parse(dailyNoteTemplate)
			if err != nil {
				fmt.Printf("  ⚠ Error parsing daily note template: %v\n", err)
				continue
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

			fmt.Printf("  ✓ Created daily note: %s (with Dataview query)\n", filename)
		}

		fmt.Printf("  ✓ Synced %d meeting file(s)\n", len(dayMeetings))
	}

	fmt.Printf("\n✅ Synced %d meeting(s) to %d daily note(s)\n", successCount, len(meetingsByDate))
	return nil
}
