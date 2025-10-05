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

	// Check for orphaned temp file from crashed save
	tempPath := path + ".new"
	if _, err := os.Stat(tempPath); err == nil {
		// Temp file exists, check if main file exists
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Main file missing but temp exists - recover from temp
			fmt.Printf("⚠ Recovering state from temp file: %s\n", tempPath)
			if err := os.Rename(tempPath, path); err != nil {
				fmt.Printf("⚠ Failed to recover from temp file: %v\n", err)
			}
		} else {
			// Both exist - temp is stale, remove it
			os.Remove(tempPath)
		}
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

// Save saves the sync state to disk atomically
func (s *SyncState) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tempPath := s.path + ".new"

	// Write to temporary file
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Rename temp file to actual file (atomic on POSIX filesystems)
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
