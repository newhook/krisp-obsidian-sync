package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

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
	stepFlag := flag.String("step", "all", "Step to run: download, summarize, sync, normalize, normalize-prompt, normalize-consume, normalize-apply, repair, or all (default: all)")
	resyncFlag := flag.Bool("resync", false, "Force re-sync all meetings to Obsidian, ignoring sync state")
	resummarizeFlag := flag.Bool("resummarize", false, "Force re-summarize all meetings, ignoring summarization state")
	testFlag := flag.Bool("test", false, "Test mode: create a single test file without updating state (sync stage only)")
	applyFlag := flag.Bool("apply", false, "Apply normalization proposal (normalize stage only)")
	meetingIDFlag := flag.String("meeting", "", "Process a specific meeting ID (for resummarize/resync single meeting)")
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

	// Create cache instance
	cache := NewCache(meetingsCacheDir)

	// Create context that cancels on Ctrl+C (SIGINT) or SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Determine which steps to run
	step := *stepFlag
	runAll := step == "all"

	// Stage 1: Download
	if runAll || step == "download" {
		if err := runDownload(ctx, *limitFlag, syncState, cache); err != nil {
			fmt.Printf("❌ Error in download stage: %v\n", err)
			return
		}
	}

	// Stage 2: Summarize
	if runAll || step == "summarize" {
		if err := runSummarize(ctx, *limitFlag, syncState, *resummarizeFlag, *meetingIDFlag, cache); err != nil {
			fmt.Printf("❌ Error in summarize stage: %v\n", err)
			return
		}
	}

	// Stage 3: Sync
	if runAll || step == "sync" {
		if err := runSync(ctx, obsidianVaultPath, *limitFlag, syncState, *resyncFlag, *testFlag, *meetingIDFlag, cache); err != nil {
			fmt.Printf("❌ Error in sync stage: %v\n", err)
			return
		}
	}

	// Stage 4: Normalize tags (3 sub-steps)
	if step == "normalize-prompt" {
		// Step 1: Generate prompt
		if err := runNormalizePrompt(ctx, cache); err != nil {
			fmt.Printf("❌ Error generating normalization prompt: %v\n", err)
			return
		}
	}

	if step == "normalize-consume" {
		// Step 2: Consume LLM result
		if err := runNormalizeConsume(ctx, cache); err != nil {
			fmt.Printf("❌ Error consuming LLM result: %v\n", err)
			return
		}
	}

	if step == "normalize-apply" || *applyFlag {
		// Step 3: Apply proposal
		if err := runNormalizeApply(ctx, cache); err != nil {
			fmt.Printf("❌ Error applying normalization: %v\n", err)
			return
		}
	}

	// Repair: Ensure all cached meetings are in sync state
	if step == "repair" {
		if err := runRepair(syncState, cache); err != nil {
			fmt.Printf("❌ Error in repair stage: %v\n", err)
			return
		}
	}

	// Update sync state
	syncState.LastSyncTime = time.Now()
	if err := syncState.Save(); err != nil {
		fmt.Printf("⚠ Warning: Could not save sync state: %v\n", err)
	}

	fmt.Println("\n✅ All requested stages completed!")
}
