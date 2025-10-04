package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
)

//go:embed summary-prompt.md
var summaryPromptTemplate string

//go:embed summary-template.md
var obsidianSummaryTemplate string

const (
	apiBaseURL       = "https://api.krisp.ai/v2"
	syncStateFile    = ".krisp_sync_state.json"
	meetingsCacheDir = "meetings"
)

var (
	bearerToken string
	gcpProject  string
	gcpLocation string
)

func main() {
	// Parse command-line flags
	limitFlag := flag.Int("limit", 1, "Number of meetings to process (default: 1 for testing)")
	stepFlag := flag.String("step", "all", "Step to run: download, summarize, sync, or all (default: all)")
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

	ctx := context.Background()

	// Determine which steps to run
	step := *stepFlag
	runAll := step == "all"

	// Stage 1: Download
	if runAll || step == "download" {
		if err := runDownload(*limitFlag, syncState, syncStatePath); err != nil {
			fmt.Printf("❌ Error in download stage: %v\n", err)
			return
		}
	}

	// Stage 2: Summarize
	if runAll || step == "summarize" {
		if err := runSummarize(*limitFlag, syncState, syncStatePath, ctx); err != nil {
			fmt.Printf("❌ Error in summarize stage: %v\n", err)
			return
		}
	}

	// Stage 3: Sync
	if runAll || step == "sync" {
		if err := runSync(obsidianVaultPath, *limitFlag); err != nil {
			fmt.Printf("❌ Error in sync stage: %v\n", err)
			return
		}
	}

	// Update sync state
	syncState.LastSyncTime = time.Now()
	if err := saveSyncState(syncStatePath, syncState); err != nil {
		fmt.Printf("⚠ Warning: Could not save sync state: %v\n", err)
	}

	fmt.Println("\n✅ All requested stages completed!")
}
