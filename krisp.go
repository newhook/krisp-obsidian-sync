package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Krisp API Response structures
type MeetingsListRequest struct {
	Sort    string `json:"sort"`
	SortKey string `json:"sortKey"`
	Page    int    `json:"page"`
	Limit   int    `json:"limit"`
	Starred bool   `json:"starred"`
}

type MeetingsListResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Rows  []MeetingSummary `json:"rows"`
		Total int              `json:"total"`
	} `json:"data"`
}

type MeetingSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"name"` // API uses "name" not "title"
	CreatedAt time.Time `json:"started_at"`
	Duration  int       `json:"duration"`
}

type Meeting struct {
	ID        string    `json:"id"`
	Title     string    `json:"name"`
	CreatedAt time.Time `json:"started_at"`
	Duration  int       `json:"duration"`
	Speakers  struct {
		Data map[string]SpeakerInfo `json:"data"` // "1", "2", etc. -> speaker info
	} `json:"speakers"`
	Resources struct {
		Transcript struct {
			Status  string `json:"status"`
			Content string `json:"content"` // JSON string containing transcript data
		} `json:"transcript"`
		MeetingNotes map[string]interface{} `json:"meeting_notes"`
	} `json:"resources"`
	Summary string `json:"summary"` // We'll populate this ourselves
	Notes   string `json:"notes"`   // We'll populate this ourselves
}

type SpeakerInfo struct {
	Person struct {
		ID        string `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Email     string `json:"email"`
	} `json:"person"`
}

type Segment struct {
	SpeakerIndex int    `json:"speakerIndex"`
	ID           int    `json:"id"`
	Speech       Speech `json:"speech"`
}

type Speech struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Krisp API functions
func fetchAllMeetings(ctx context.Context) ([]MeetingSummary, error) {
	var allMeetings []MeetingSummary
	page := 1
	limit := 100

	for {
		// Check if context was cancelled
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		requestBody := MeetingsListRequest{
			Sort:    "asc", // Get oldest first
			SortKey: "created_at",
			Page:    page,
			Limit:   limit,
			Starred: false,
		}

		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", apiBaseURL+"/meetings/list", bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}

		setHeaders(req)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		}

		var listResp MeetingsListResponse
		if err := json.Unmarshal(body, &listResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		allMeetings = append(allMeetings, listResp.Data.Rows...)

		// Continue if we got a full page of results
		if len(listResp.Data.Rows) < limit {
			break
		}

		page++
	}

	return allMeetings, nil
}

func fetchMeeting(ctx context.Context, meetingID string) (*Meeting, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiBaseURL+"/meetings/"+meetingID, nil)
	if err != nil {
		return nil, err
	}

	setHeaders(req)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// The API wraps the meeting in a data object
	var response struct {
		Code    int     `json:"code"`
		Message string  `json:"message"`
		Data    Meeting `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	return &response.Data, nil
}

func setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("krisp_header_app", "web")
	req.Header.Set("krisp_header_web_project", "note")
	req.Header.Set("krisp_origin_timezone", "-04:00")
	req.Header.Set("Origin", "https://app.krisp.ai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
}
