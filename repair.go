package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// runRepair ensures sync state matches the actual filesystem state
func runRepair(syncState *SyncState, cache *Cache) error {
	fmt.Println("\n=== Repair: Syncing state with filesystem ===")

	// Get all meeting files from filesystem
	files, err := filepath.Glob(filepath.Join(meetingsCacheDir, "*.json"))
	if err != nil {
		return fmt.Errorf("error reading cache directory: %w", err)
	}

	// Build sets of what actually exists
	actualMeetings := make(map[string]bool)
	actualSummaries := make(map[string]bool)

	for _, file := range files {
		filename := filepath.Base(file)

		if strings.HasSuffix(filename, "-summary.json") {
			// Summary file
			meetingID := strings.TrimSuffix(filename, "-summary.json")
			actualSummaries[meetingID] = true
		} else {
			// Meeting file
			meetingID := strings.TrimSuffix(filename, ".json")
			actualMeetings[meetingID] = true
		}
	}

	// Rebuild SyncedMeetings to match filesystem
	addedCount := 0
	for meetingID := range actualMeetings {
		if !syncState.SyncedMeetings[meetingID] {
			syncState.SyncedMeetings[meetingID] = true
			addedCount++
			fmt.Printf("  ✓ Added to sync state: %s\n", meetingID)
		}
	}

	// Rebuild SummarizedMeetings to match filesystem
	oldSummarizedCount := len(syncState.SummarizedMeetings)
	syncState.SummarizedMeetings = make(map[string]bool)
	for meetingID := range actualSummaries {
		syncState.SummarizedMeetings[meetingID] = true
	}
	newSummarizedCount := len(syncState.SummarizedMeetings)

	// Clear ObsidianSyncedMeetings - let user re-sync
	oldObsidianCount := len(syncState.ObsidianSyncedMeetings)
	syncState.ObsidianSyncedMeetings = make(map[string]bool)

	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Meetings in filesystem: %d\n", len(actualMeetings))
	fmt.Printf("  Summaries in filesystem: %d\n", len(actualSummaries))
	fmt.Printf("  Summarized state: %d → %d\n", oldSummarizedCount, newSummarizedCount)
	fmt.Printf("  Obsidian synced state: %d → 0 (cleared)\n", oldObsidianCount)

	// Save updated state
	if err := syncState.Save(); err != nil {
		return fmt.Errorf("error saving sync state: %w", err)
	}

	fmt.Printf("\n✅ Repair complete - state now matches filesystem\n")
	return nil
}
