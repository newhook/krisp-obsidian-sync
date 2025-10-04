package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Sync state to track last sync
type SyncState struct {
	LastSyncTime           time.Time       `json:"last_sync_time"`
	SyncedMeetings         map[string]bool `json:"synced_meetings"`          // meeting ID -> downloaded from Krisp
	SummarizedMeetings     map[string]bool `json:"summarized_meetings"`      // meeting ID -> summarized with Gemini
	ObsidianSyncedMeetings map[string]bool `json:"obsidian_synced_meetings"` // meeting ID -> synced to Obsidian vault

	// Internal field to remember the file path (not serialized to JSON)
	path string `json:"-"`
}

func loadSyncState(path string) *SyncState {
	state := &SyncState{
		SyncedMeetings:         make(map[string]bool),
		SummarizedMeetings:     make(map[string]bool),
		ObsidianSyncedMeetings: make(map[string]bool),
		path:                   path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist, return empty state
		return state
	}

	if err := json.Unmarshal(data, state); err != nil {
		fmt.Printf("⚠ Warning: Could not parse sync state, starting fresh: %v\n", err)
		return &SyncState{
			SyncedMeetings:         make(map[string]bool),
			SummarizedMeetings:     make(map[string]bool),
			ObsidianSyncedMeetings: make(map[string]bool),
			path:                   path,
		}
	}

	// Ensure maps are initialized (for backwards compatibility)
	if state.SyncedMeetings == nil {
		state.SyncedMeetings = make(map[string]bool)
	}
	if state.SummarizedMeetings == nil {
		state.SummarizedMeetings = make(map[string]bool)
	}
	if state.ObsidianSyncedMeetings == nil {
		state.ObsidianSyncedMeetings = make(map[string]bool)
	}

	// Remember the path
	state.path = path

	return state
}

// Save saves the sync state to disk
func (s *SyncState) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}
