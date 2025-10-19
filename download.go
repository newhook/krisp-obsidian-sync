package main

import (
	"context"
	"fmt"
)

// Stage 1: Download meetings from Krisp API and cache them locally
func runDownload(ctx context.Context, limit int, syncState *SyncState, overwrite bool, meetingIDs []string, cache *Cache) error {
	fmt.Println("\n=== Stage 1: Downloading meetings ===")

	// Handle specific meeting IDs mode
	if len(meetingIDs) > 0 {
		fmt.Printf("🎯 Re-downloading %d specific meeting(s) from Krisp API\n", len(meetingIDs))
		for _, meetingID := range meetingIDs {
			fullMeeting, err := fetchMeeting(ctx, meetingID)
			if err != nil {
				fmt.Printf("❌ Error fetching meeting %s: %v\n", meetingID, err)
				continue
			}

			// Save to cache (overwriting existing)
			if err := cache.SaveMeeting(fullMeeting); err != nil {
				fmt.Printf("  ⚠ Error saving to cache: %v\n", err)
				continue
			}

			syncState.SyncedMeetings[fullMeeting.ID] = true
			fmt.Printf("  ✓ Re-downloaded and cached: %s\n", meetingID)

			// Save state
			if err := syncState.Save(); err != nil {
				fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
			}
		}
		fmt.Printf("\n✅ Re-downloaded %d meeting(s)\n", len(meetingIDs))
		return nil
	}

	// Fetch all meetings from API
	allMeetings, err := fetchAllMeetings(ctx)
	if err != nil {
		return fmt.Errorf("error fetching meetings: %w", err)
	}

	fmt.Printf("📊 Total meetings fetched from API: %d\n", len(allMeetings))

	// Filter to only meetings not yet downloaded (unless overwrite is set)
	var toDownload []MeetingSummary
	for _, m := range allMeetings {
		if overwrite || !cache.MeetingExists(m.ID) {
			toDownload = append(toDownload, m)
		}
	}

	if overwrite && len(toDownload) > 0 {
		fmt.Printf("🔄 Overwrite mode: will re-download all %d meetings\n", len(toDownload))
	}

	if len(toDownload) == 0 {
		fmt.Println("✅ All meetings already cached!")
		return nil
	}

	fmt.Printf("Found %d meeting(s) to download\n", len(toDownload))

	// Apply limit
	if limit > 0 && len(toDownload) > limit {
		fmt.Printf("⚠ Limiting to %d meeting(s) for this run\n", limit)
		toDownload = toDownload[:limit]
	}

	// Download and cache each meeting
	for i, meetingSummary := range toDownload {
		// Check if context was cancelled
		if ctx.Err() != nil {
			fmt.Printf("\n⚠ Download cancelled\n")
			return ctx.Err()
		}

		fmt.Printf("[%d/%d] Downloading: %s\n", i+1, len(toDownload), meetingSummary.Title)

		fullMeeting, err := fetchMeeting(ctx, meetingSummary.ID)
		if err != nil {
			fmt.Printf("  ⚠ Error fetching meeting: %v\n", err)
			continue
		}

		// Save to cache
		if err := cache.SaveMeeting(fullMeeting); err != nil {
			fmt.Printf("  ⚠ Error saving to cache: %v\n", err)
			continue
		}

		syncState.SyncedMeetings[fullMeeting.ID] = true
		fmt.Printf("  ✓ Cached: meetings/%s.json\n", fullMeeting.ID)

		// Save state after each download
		if err := syncState.Save(); err != nil {
			fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
		}
	}

	fmt.Printf("\n✅ Downloaded %d meeting(s)\n", len(toDownload))
	return nil
}
