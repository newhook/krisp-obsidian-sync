package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SummaryData holds the structured summary information
type SummaryData struct {
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Summary     string `json:"summary"`
}

// Cache manages local storage of meetings and summaries with in-memory caching
type Cache struct {
	dir            string
	meetings       map[string]*Meeting
	summaries      map[string]*SummaryData
	dirInitialized bool
}

// NewCache creates a new cache instance
func NewCache(dir string) *Cache {
	return &Cache{
		dir:       dir,
		meetings:  make(map[string]*Meeting),
		summaries: make(map[string]*SummaryData),
	}
}

// ensureDir creates the cache directory if it doesn't exist
func (c *Cache) ensureDir() error {
	if c.dirInitialized {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}
	c.dirInitialized = true
	return nil
}

// SaveMeeting saves a meeting to disk and cache
func (c *Cache) SaveMeeting(meeting *Meeting) error {
	if err := c.ensureDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(meeting, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal meeting: %w", err)
	}

	cachePath := filepath.Join(c.dir, meeting.ID+".json")
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	// Cache in memory
	c.meetings[meeting.ID] = meeting
	return nil
}

// LoadMeeting loads a meeting from cache (memory first, then disk)
func (c *Cache) LoadMeeting(meetingID string) (*Meeting, error) {
	// Check in-memory cache first
	if meeting, ok := c.meetings[meetingID]; ok {
		return meeting, nil
	}

	// Load from disk
	cachePath := filepath.Join(c.dir, meetingID+".json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var meeting Meeting
	if err := json.Unmarshal(data, &meeting); err != nil {
		return nil, fmt.Errorf("failed to unmarshal meeting: %w", err)
	}

	// Cache in memory
	c.meetings[meetingID] = &meeting
	return &meeting, nil
}

// MeetingExists checks if a meeting exists in cache
func (c *Cache) MeetingExists(meetingID string) bool {
	// Check memory first
	if _, ok := c.meetings[meetingID]; ok {
		return true
	}

	// Check disk
	cachePath := filepath.Join(c.dir, meetingID+".json")
	_, err := os.Stat(cachePath)
	return err == nil
}

// SaveSummary saves a summary to disk and cache
func (c *Cache) SaveSummary(meetingID string, summary *SummaryData) error {
	if err := c.ensureDir(); err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal summary data: %w", err)
	}

	cachePath := filepath.Join(c.dir, meetingID+"-summary.json")
	if err := os.WriteFile(cachePath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write summary data file: %w", err)
	}

	// Cache in memory
	c.summaries[meetingID] = summary
	return nil
}

// LoadSummary loads a summary from cache (memory first, then disk)
func (c *Cache) LoadSummary(meetingID string) (*SummaryData, error) {
	// Check in-memory cache first
	if summary, ok := c.summaries[meetingID]; ok {
		return summary, nil
	}

	// Load from disk
	cachePath := filepath.Join(c.dir, meetingID+"-summary.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read summary data file: %w", err)
	}

	var summaryData SummaryData
	if err := json.Unmarshal(data, &summaryData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal summary data: %w", err)
	}

	// Cache in memory
	c.summaries[meetingID] = &summaryData
	return &summaryData, nil
}

// SummaryExists checks if a summary exists in cache
func (c *Cache) SummaryExists(meetingID string) bool {
	// Check memory first
	if _, ok := c.summaries[meetingID]; ok {
		return true
	}

	// Check disk
	cachePath := filepath.Join(c.dir, meetingID+"-summary.json")
	_, err := os.Stat(cachePath)
	return err == nil
}
