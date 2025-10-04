package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

const (
	apiBaseURL    = "https://api.krisp.ai/v2"
	syncStateFile = ".krisp_sync_state.json"
)

var (
	bearerToken      string
	gcpProject       string
	gcpLocation      string
)

// Sync state to track last sync
type SyncState struct {
	LastSyncTime       time.Time       `json:"last_sync_time"`
	SyncedMeetings     map[string]bool `json:"synced_meetings"`     // meeting ID -> downloaded from Krisp
	SummarizedMeetings map[string]bool `json:"summarized_meetings"` // meeting ID -> summarized with Gemini
}

// API Response structures
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

func main() {
	// Parse command-line flags
	limitFlag := flag.Int("limit", 1, "Number of meetings to process (default: 1 for testing)")
	flag.Parse()

	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	bearerToken = os.Getenv("KRISP_BEARER_TOKEN")
	if bearerToken == "" {
		log.Fatal("KRISP_BEARER_TOKEN not set in .env file")
	}

	gcpProject = os.Getenv("GOOGLE_CLOUD_PROJECT")
	if gcpProject == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT not set in .env file")
	}

	gcpLocation = os.Getenv("GOOGLE_CLOUD_LOCATION")
	if gcpLocation == "" {
		log.Fatal("GOOGLE_CLOUD_LOCATION not set in .env file")
	}

	// Configuration
	obsidianVaultPath := "/Users/matthew/Documents/Obsidian Vault"

	// Store sync state in application directory
	syncStatePath := filepath.Join(".", syncStateFile)

	// Load sync state
	syncState := loadSyncState(syncStatePath)
	isFirstSync := syncState.LastSyncTime.IsZero()

	if isFirstSync {
		fmt.Println("🆕 First sync - will download all meetings")
	} else {
		fmt.Printf("🔄 Last sync: %s\n", syncState.LastSyncTime.Format("2006-01-02 15:04:05"))
	}

	fmt.Println("Fetching meetings from Krisp.ai...")

	// Fetch all meetings
	allMeetings, err := fetchAllMeetings()
	if err != nil {
		fmt.Printf("Error fetching meetings: %v\n", err)
		return
	}

	fmt.Printf("📊 Total meetings fetched from API: %d\n", len(allMeetings))

	// Filter to only new meetings (not yet downloaded OR not yet summarized)
	var newMeetings []MeetingSummary
	for _, m := range allMeetings {
		// Include if not downloaded yet OR not summarized yet
		if !syncState.SyncedMeetings[m.ID] || !syncState.SummarizedMeetings[m.ID] {
			newMeetings = append(newMeetings, m)
		}
	}

	if len(newMeetings) == 0 {
		fmt.Println("✅ Already up to date! No new meetings to sync.")
		return
	}

	fmt.Printf("Found %d meeting(s) to process\n", len(newMeetings))

	// Apply limit flag
	if *limitFlag > 0 && len(newMeetings) > *limitFlag {
		fmt.Printf("⚠ Limiting to %d meeting(s) for this run\n", *limitFlag)
		newMeetings = newMeetings[:*limitFlag]
	}

	// Group meetings by date
	meetingsByDate := make(map[string][]*Meeting)
	ctx := context.Background()

	for i, meeting := range newMeetings {
		fmt.Printf("[%d/%d] Processing: %s\n", i+1, len(newMeetings), meeting.Title)

		// Step 1: Fetch meeting from Krisp (if not already fetched)
		var fullMeeting *Meeting
		if !syncState.SyncedMeetings[meeting.ID] {
			fmt.Printf("  📥 Downloading from Krisp...\n")
			var err error
			fullMeeting, err = fetchMeeting(meeting.ID)
			if err != nil {
				fmt.Printf("  ⚠ Error fetching meeting details: %v\n", err)
				continue
			}
			syncState.SyncedMeetings[fullMeeting.ID] = true

			// Save state after downloading
			if err := saveSyncState(syncStatePath, syncState); err != nil {
				fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
			}
		} else {
			fmt.Printf("  ✓ Already downloaded\n")
			var err error
			fullMeeting, err = fetchMeeting(meeting.ID)
			if err != nil {
				fmt.Printf("  ⚠ Error re-fetching meeting details: %v\n", err)
				continue
			}
		}

		// Step 2: Summarize with Gemini (if not already summarized)
		if !syncState.SummarizedMeetings[fullMeeting.ID] {
			fmt.Printf("  🤖 Generating summary with Gemini...\n")

			// Parse and build transcript text from segments
			var transcriptText string
			if fullMeeting.Resources.Transcript.Status == "uploaded" && fullMeeting.Resources.Transcript.Content != "" {
				var segments []Segment
				if err := json.Unmarshal([]byte(fullMeeting.Resources.Transcript.Content), &segments); err != nil {
					fmt.Printf("  ⚠ Error parsing transcript: %v\n", err)
				} else if len(segments) > 0 {
					var sb strings.Builder
					for _, seg := range segments {
						sb.WriteString(fmt.Sprintf("Speaker %d: %s\n", seg.SpeakerIndex, seg.Speech.Text))
					}
					transcriptText = sb.String()
				}
			}

			if transcriptText != "" {
				geminiSummary, err := summarizeWithGemini(ctx, transcriptText)
				if err != nil {
					fmt.Printf("  ⚠ Error generating summary: %v\n", err)
				} else {
					// Store Gemini's summary
					fullMeeting.Summary = geminiSummary
					fmt.Printf("  ✓ Summary generated\n")
				}
			} else {
				fmt.Printf("  ⚠ No transcript available for summarization\n")
			}

			syncState.SummarizedMeetings[fullMeeting.ID] = true

			// Save state after summarizing
			if err := saveSyncState(syncStatePath, syncState); err != nil {
				fmt.Printf("  ⚠ Warning: Could not save sync state: %v\n", err)
			}
		} else {
			fmt.Printf("  ✓ Already summarized\n")
		}

		// Step 3: Group by date for saving to Obsidian
		dateKey := fullMeeting.CreatedAt.Format("2006-01-02")
		meetingsByDate[dateKey] = append(meetingsByDate[dateKey], fullMeeting)

		// Be nice to the APIs
		time.Sleep(500 * time.Millisecond)
	}

	// Process each day
	successCount := 0
	for date, dayMeetings := range meetingsByDate {
		fmt.Printf("\n📅 Processing %s (%d meeting(s))\n", date, len(dayMeetings))

		// Sort meetings by time
		sort.Slice(dayMeetings, func(i, j int) bool {
			return dayMeetings[i].CreatedAt.Before(dayMeetings[j].CreatedAt)
		})

		// Generate path: YYYY/MM-MonthName/YYYY-MM-DD-DayName.md
		t := dayMeetings[0].CreatedAt
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

		// Create individual meeting files and collect links
		var meetingLinks []string
		for _, m := range dayMeetings {
			// Create meeting file: YYYY-MM-DD-HHMM-Meeting-Title.md
			timeStr := m.CreatedAt.Format("1504") // HHMM format
			safeName := strings.ReplaceAll(m.Title, "/", "-")
			safeName = strings.ReplaceAll(safeName, ":", "-")
			meetingFileName := fmt.Sprintf("%s-%s-%s.md", date, timeStr, safeName)
			meetingFilePath := filepath.Join(meetingsPath, meetingFileName)

			// Generate meeting content
			meetingContent := generateMeetingContent(m)

			// Write meeting file
			if err := os.WriteFile(meetingFilePath, []byte(meetingContent), 0644); err != nil {
				fmt.Printf("  ⚠ Error writing meeting file: %v\n", err)
				continue
			}

			// Add link to daily note (with meetings/ path)
			meetingLinks = append(meetingLinks, fmt.Sprintf("- [[meetings/%s|%s - %s]]",
				meetingFileName[:len(meetingFileName)-3], // Remove .md extension
				m.CreatedAt.Format("3:04 PM"),
				m.Title))
		}

		// Update daily note with meeting links
		filename := fmt.Sprintf("%s-%s.md", date, dayName)
		filePath := filepath.Join(dailyNotesPath, filename)

		// Check if daily note already exists
		existingContent := ""
		if data, err := os.ReadFile(filePath); err == nil {
			existingContent = string(data)
		}

		// Generate content with meeting links
		meetingsContent := strings.Join(meetingLinks, "\n") + "\n\n"

		// Append or create the daily note
		var finalContent string
		if existingContent != "" {
			// Append to existing note
			finalContent = appendToExistingNote(existingContent, meetingsContent)
		} else {
			// Create new daily note
			finalContent = createNewDailyNote(date, meetingsContent)
		}

		if err := os.WriteFile(filePath, []byte(finalContent), 0644); err != nil {
			fmt.Printf("  ⚠ Error writing file: %v\n", err)
			continue
		}

		fmt.Printf("  ✓ Updated: %s (%d meeting file(s))\n", filename, len(dayMeetings))
		successCount += len(dayMeetings)
	}

	// Update sync state
	syncState.LastSyncTime = time.Now()
	if err := saveSyncState(syncStatePath, syncState); err != nil {
		fmt.Printf("⚠ Warning: Could not save sync state: %v\n", err)
	}

	fmt.Printf("\n✅ Sync completed! %d meeting(s) added to %d daily note(s).\n", successCount, len(meetingsByDate))
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

func fetchAllMeetings() ([]MeetingSummary, error) {
	var allMeetings []MeetingSummary
	page := 1
	limit := 20

	for {
		requestBody := MeetingsListRequest{
			Sort:    "asc",  // Get oldest first
			SortKey: "created_at",
			Page:    page,
			Limit:   limit,
			Starred: false,
		}

		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest("POST", apiBaseURL+"/meetings/list", bytes.NewBuffer(jsonData))
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

		// Debug output
		fmt.Printf("DEBUG: Page %d - Got %d meetings, Total in response: %d, Accumulated: %d\n",
			page, len(listResp.Data.Rows), listResp.Data.Total, len(allMeetings))

		// Continue if we got a full page of results
		if len(listResp.Data.Rows) < limit {
			break
		}

		page++
	}

	return allMeetings, nil
}

func fetchMeeting(meetingID string) (*Meeting, error) {
	req, err := http.NewRequest("GET", apiBaseURL+"/meetings/"+meetingID, nil)
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

func createNewDailyNote(date string, meetingsContent string) string {
	var sb strings.Builder

	// Basic daily note structure
	sb.WriteString("# " + date + "\n\n")
	sb.WriteString("## Meetings\n\n")
	sb.WriteString(meetingsContent)

	return sb.String()
}

func appendToExistingNote(existingContent, meetingsContent string) string {
	// Check if there's already a Meetings section
	if strings.Contains(existingContent, "## Meetings") {
		// Find the Meetings section and append there
		lines := strings.Split(existingContent, "\n")
		var result strings.Builder
		inMeetingsSection := false
		meetingsAdded := false

		for i, line := range lines {
			result.WriteString(line + "\n")

			if strings.HasPrefix(line, "## Meetings") {
				inMeetingsSection = true
			} else if inMeetingsSection && strings.HasPrefix(line, "## ") {
				// Found next section, insert meetings before it
				if !meetingsAdded {
					result.WriteString(meetingsContent)
					meetingsAdded = true
				}
				inMeetingsSection = false
			}

			// If we're at the end and still in meetings section
			if inMeetingsSection && i == len(lines)-1 && !meetingsAdded {
				result.WriteString(meetingsContent)
				meetingsAdded = true
			}
		}

		// If meetings section was at the end
		if inMeetingsSection && !meetingsAdded {
			result.WriteString(meetingsContent)
		}

		return result.String()
	} else {
		// No Meetings section exists, append it at the end
		return existingContent + "\n## Meetings\n\n" + meetingsContent
	}
}

func generateMeetingContent(m *Meeting) string {
	var sb strings.Builder

	// Meeting header
	timeStr := m.CreatedAt.Format("3:04 PM")
	dateStr := m.CreatedAt.Format("Monday, January 2, 2006")
	sb.WriteString(fmt.Sprintf("# %s - %s\n\n", timeStr, m.Title))
	sb.WriteString(fmt.Sprintf("**Date**: %s\n", dateStr))

	// Metadata
	if m.Duration > 0 {
		minutes := m.Duration / 60
		seconds := m.Duration % 60
		sb.WriteString(fmt.Sprintf("**Duration**: %d:%02d\n", minutes, seconds))
	}
	sb.WriteString(fmt.Sprintf("**Meeting ID**: `%s`\n\n", m.ID))

	// Summary
	if m.Summary != "" {
		sb.WriteString("## Summary\n\n")
		sb.WriteString(m.Summary + "\n\n")
	}

	// Notes
	if m.Notes != "" {
		sb.WriteString("## Notes\n\n")
		sb.WriteString(m.Notes + "\n\n")
	}

	// Transcript in collapsible section
	if m.Resources.Transcript.Status == "uploaded" && m.Resources.Transcript.Content != "" {
		var segments []Segment
		if err := json.Unmarshal([]byte(m.Resources.Transcript.Content), &segments); err == nil && len(segments) > 0 {
			sb.WriteString("## Transcript\n\n")
			sb.WriteString("<details>\n<summary>📝 Full Transcript</summary>\n\n")

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

			sb.WriteString("</details>\n\n")
		}
	}

	return sb.String()
}

func formatTimestamp(seconds float64) string {
	totalSeconds := int(seconds)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	secs := totalSeconds % 60

	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

func summarizeWithGemini(ctx context.Context, transcript string) (string, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  gcpProject,
		Location: gcpLocation,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create Vertex AI client: %w", err)
	}

	prompt := fmt.Sprintf(`Please provide a concise summary of the following meeting transcript.
Focus on key points, decisions made, and action items.

Transcript:
%s

Summary:`, transcript)

	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash-lite", []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				genai.NewPartFromText(prompt),
			},
		},
	}, &genai.GenerateContentConfig{
		Temperature:     func() *float32 { v := float32(0.3); return &v }(),
		MaxOutputTokens: 2048,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no summary generated")
	}

	summary := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0].Text)
	return summary, nil
}