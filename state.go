package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Sync state to track last sync
type SyncState struct {
	LastSyncTime       time.Time       `json:"last_sync_time"`
	SyncedMeetings     map[string]bool `json:"synced_meetings"`     // meeting ID -> downloaded from Krisp
	SummarizedMeetings map[string]bool `json:"summarized_meetings"` // meeting ID -> summarized with Gemini
}

func loadSyncState(path string) *SyncState {
	state := &SyncState{
		SyncedMeetings:     make(map[string]bool),
		SummarizedMeetings: make(map[string]bool),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist, return empty state
		return state
	}

	if err := json.Unmarshal(data, state); err != nil {
		fmt.Printf("⚠ Warning: Could not parse sync state, starting fresh: %v\n", err)
		return &SyncState{
			SyncedMeetings:     make(map[string]bool),
			SummarizedMeetings: make(map[string]bool),
		}
	}

	// Ensure maps are initialized (for backwards compatibility)
	if state.SyncedMeetings == nil {
		state.SyncedMeetings = make(map[string]bool)
	}
	if state.SummarizedMeetings == nil {
		state.SummarizedMeetings = make(map[string]bool)
	}

	return state
}

func saveSyncState(path string, state *SyncState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
