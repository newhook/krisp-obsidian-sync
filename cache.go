package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Cache helper functions
func saveMeetingToCache(meeting *Meeting) error {
	if err := os.MkdirAll(meetingsCacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	data, err := json.MarshalIndent(meeting, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal meeting: %w", err)
	}

	cachePath := filepath.Join(meetingsCacheDir, meeting.ID+".json")
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

func loadMeetingFromCache(meetingID string) (*Meeting, error) {
	cachePath := filepath.Join(meetingsCacheDir, meetingID+".json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var meeting Meeting
	if err := json.Unmarshal(data, &meeting); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
	}

	return &meeting, nil
}

func saveSummaryToCache(meetingID, summary string) error {
	if err := os.MkdirAll(meetingsCacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	cachePath := filepath.Join(meetingsCacheDir, meetingID+"-summary.md")
	if err := os.WriteFile(cachePath, []byte(summary), 0644); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}

	return nil
}

func loadSummaryFromCache(meetingID string) (string, error) {
	cachePath := filepath.Join(meetingsCacheDir, meetingID+"-summary.md")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return "", fmt.Errorf("failed to read summary file: %w", err)
	}

	return string(data), nil
}

func meetingExistsInCache(meetingID string) bool {
	cachePath := filepath.Join(meetingsCacheDir, meetingID+".json")
	_, err := os.Stat(cachePath)
	return err == nil
}

func summaryExistsInCache(meetingID string) bool {
	cachePath := filepath.Join(meetingsCacheDir, meetingID+"-summary.md")
	_, err := os.Stat(cachePath)
	return err == nil
}
