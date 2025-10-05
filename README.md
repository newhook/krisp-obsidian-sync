# krisp-sync

A tool to sync meeting recordings from Krisp.ai to your Obsidian vault with AI-generated summaries.

## Purpose

`krisp-sync` downloads meeting recordings from the Krisp.ai API, generates structured summaries using Google's Gemini AI, and syncs everything to your Obsidian vault with organized daily notes and automatic tag management.

## Features

- **Three-stage pipeline**: Download meetings, generate AI summaries, sync to Obsidian
- **Incremental syncing**: Only processes new/changed meetings
- **AI-powered summaries**: Uses Gemini to create structured summaries with topics, descriptions, and tags
- **Tag normalization**: Consolidates similar tags across meetings for consistency
- **Obsidian integration**: Creates daily notes with Dataview queries to automatically list meetings
- **Graceful interruption**: Supports Ctrl+C cancellation with state preservation
- **Resumable**: State tracking allows you to resume interrupted operations

## Prerequisites

1. **Krisp.ai account** with API access
2. **Google Cloud project** with Vertex AI enabled
3. **Obsidian vault** (local directory)
4. **Go 1.21+** installed

## Setup

1. Create a `.env` file in the project directory:

```env
KRISP_BEARER_TOKEN=your_krisp_api_token
GOOGLE_CLOUD_PROJECT=your-gcp-project-id
GOOGLE_CLOUD_LOCATION=us-central1
```

2. Update the Obsidian vault path in `main.go`:

```go
obsidianVaultPath := "/path/to/your/Obsidian Vault"
```

3. Build the project:

```bash
go build -o krisp-sync .
```

## Command Line Options

### Basic Usage

```bash
./krisp-sync [flags]
```

### Flags

- `--step <stage>` - Run a specific stage (default: `all`)
  - `all` - Run all stages in sequence
  - `download` - Download meetings from Krisp API to local cache
  - `summarize` - Generate AI summaries for cached meetings
  - `sync` - Sync cached meetings and summaries to Obsidian
  - `normalize-prompt` - Generate tag normalization prompt
  - `normalize-consume` - Consume LLM result and create proposal
  - `normalize-apply` - Apply normalization proposal and write tags dictionary
  - `repair` - Sync filesystem state with tracking state

- `--limit <n>` - Number of meetings to process (default: `1` for testing)
  - Set to `0` to process all available meetings
  - Useful for testing with small batches first

- `--resync` - Force re-sync all meetings to Obsidian, ignoring sync state
  - Clears the Obsidian sync tracking
  - Does not re-download or re-summarize
  - Useful if you've modified your Obsidian templates

- `--resummarize` - Force re-summarize all meetings, ignoring summarization state
  - Clears the summarization tracking
  - Does not re-download meetings
  - Useful if you've modified your summary prompt or want to regenerate summaries

- `--test` - Test mode for sync stage only
  - Creates a single test file without updating state
  - Processes the oldest meeting
  - Can be run repeatedly for testing templates
  - Does not mark meetings as synced

- `--meeting <meeting-id>` - Process a specific meeting by ID
  - Works with `--resummarize` to re-summarize a single meeting
  - Works with `--resync` to re-sync a single meeting to Obsidian
  - Useful for fixing issues with individual meetings

## How It Works

### Stage 1: Download

Downloads meeting recordings from the Krisp.ai API and caches them locally as JSON files.

```bash
./krisp-sync --step download --limit 10
```

- Fetches meeting metadata and full transcripts
- Saves to `meetings/<meeting-id>.json`
- Tracks downloaded meetings in `.krisp_sync_state.json`
- Skips meetings already in cache

### Stage 2: Summarize

Generates AI summaries using Google Gemini for each cached meeting.

```bash
./krisp-sync --step summarize --limit 10
```

- Processes meetings in chronological order (oldest to newest)
- Uses meeting transcripts to generate:
  - One-line description
  - Relevant tags (using existing tag dictionary if available)
  - List of topics discussed
  - Detailed summaries for each topic
- Saves summaries to `meetings/<meeting-id>-summary.json`
- Tracks summarized meetings in state file

### Stage 3: Sync

Syncs meetings and summaries to your Obsidian vault.

```bash
./krisp-sync --step sync --limit 10
```

**Output structure:**
```
Obsidian Vault/
├── 2025/
│   └── 09-September/
│       ├── 2025-09-15-Friday.md       # Daily note with Dataview query
│       └── meetings/
│           ├── <meeting-id>-summary.md     # Meeting summary with frontmatter
│           └── <meeting-id>-transcript.md  # Full transcript
```

**Daily notes** include a Dataview query that automatically lists all meetings:
```markdown
# 2025-09-15

## Meetings

```dataview
TABLE WITHOUT ID
  file.link as "Meeting",
  time as "Time",
  participants as "Participants"
FROM "2025/09-September/meetings"
WHERE type = "meeting" AND date = date("2025-09-15")
SORT time ASC
```
```

- Creates summary and transcript files for each meeting
- Generates daily notes with Dataview queries
- Skips existing files (never overwrites)
- Tracks synced meetings in state file

### Stage 4: Normalize Tags

Consolidates tags across all summaries for consistency using a 3-step workflow.

#### Step 4.1: Generate Normalization Prompt

```bash
./krisp-sync --step normalize-prompt
```

- Analyzes all tags used across meetings
- Performs fuzzy matching to pre-consolidate obvious duplicates
- Generates a prompt file for LLM processing
- Outputs:
  - `normalize-prompt-generated.txt` - Prompt to send to your LLM
  - `normalize-premappings.json` - Pre-consolidated tag mappings

#### Step 4.2: Process with LLM (Manual)

Run your preferred LLM (e.g., Claude, Gemini) on the generated prompt. The LLM will consolidate semantically related tags. Save the result to `llm-result.json` in this format:

```json
[
  {
    "canonical_tag": "product-strategy",
    "old_tags": ["product-strategy", "product-roadmap", "product-vision", ...]
  },
  ...
]
```

#### Step 4.3: Consume LLM Result

```bash
./krisp-sync --step normalize-consume
```

- Reads `llm-result.json` and `normalize-premappings.json`
- Merges LLM mappings with fuzzy pre-mappings
- Creates `tags-proposal.json` for review

#### Step 4.4: Apply Normalization

```bash
./krisp-sync --step normalize-apply
```

- Reads `tags-proposal.json`
- Writes `tags-dictionary.json`
- Tag mappings are applied when syncing to Obsidian (summaries remain unchanged)
- Future summaries will prefer existing canonical tags

## Common Workflows

### First-time sync of all meetings

```bash
# Download all meetings
./krisp-sync --step download --limit 0

# Summarize all meetings
./krisp-sync --step summarize --limit 0

# Normalize tags before syncing
./krisp-sync --step normalize-prompt
# (Process prompt with LLM, save to llm-result.json)
./krisp-sync --step normalize-consume
./krisp-sync --step normalize-apply

# Sync to Obsidian
./krisp-sync --step sync --limit 0
```

### Incremental sync (daily use)

```bash
# Run all stages for new meetings only
./krisp-sync --limit 0
```

### Testing with small batches

```bash
# Process just 5 meetings to test
./krisp-sync --limit 5
```

### Re-sync to Obsidian after template changes

```bash
# Delete meeting files from vault, then:
./krisp-sync --step sync --resync --limit 0
```

### Re-generate summaries after prompt changes

```bash
# Clear cached summaries, then:
rm meetings/*-summary.json
./krisp-sync --step summarize --resummarize --limit 0
```

### Re-summarize and re-sync a single meeting

```bash
# Re-summarize a specific meeting that had issues
./krisp-sync --step summarize --meeting fd00fb02629c46d0981c968a5565ecc6 --resummarize

# Re-sync a specific meeting to Obsidian
./krisp-sync --step sync --meeting fd00fb02629c46d0981c968a5565ecc6 --resync
```

### Test workflow with single meeting

```bash
# Summarize one meeting
./krisp-sync --step summarize --limit 1

# Test sync output
./krisp-sync --step sync --test
```

### Fix tag inconsistencies

```bash
# Re-run normalization workflow
./krisp-sync --step normalize-prompt
# (Process prompt with LLM, save to llm-result.json)
./krisp-sync --step normalize-consume
# (Review tags-proposal.json)
./krisp-sync --step normalize-apply

# Re-sync summaries with updated tags
./krisp-sync --step sync --resync --limit 0
```

## State File

The `.krisp_sync_state.json` file tracks:
- `synced_meetings` - Meetings downloaded from Krisp
- `summarized_meetings` - Meetings with AI summaries
- `obsidian_synced_meetings` - Meetings written to Obsidian
- `last_sync_time` - Timestamp of last successful sync

This allows incremental syncing and graceful recovery from interruptions.

## Customization

### Templates

Templates are embedded in the source code:

- `summary-prompt.md` - Prompt for Gemini summary generation
- `summary-template.md` - Obsidian frontmatter template for meeting summaries
- `daily-note-template.md` - Template for daily notes
- `normalize-prompt.md` - Prompt for tag normalization

Edit these files and rebuild to customize output.

### Obsidian Vault Path

Edit `main.go` to change the vault location:

```go
obsidianVaultPath := "/Users/yourname/Documents/Obsidian Vault"
```

## Troubleshooting

### "No cached meetings found"

Run the download stage first:
```bash
./krisp-sync --step download --limit 10
```

### "All meetings already synced"

Either all meetings are up to date, or use `--resync` to force re-sync:
```bash
./krisp-sync --step sync --resync
```

### Ctrl+C during operation

The state is saved after each meeting is processed, so you can safely resume where you left off.

### Rate limiting / API errors

The tool includes 500ms delays between API calls. If you encounter rate limits, run smaller batches with `--limit`.

## Development

### Project Structure

- `main.go` - Entry point, CLI parsing, embedded templates
- `krisp.go` - Krisp API client
- `download.go` - Stage 1: Download meetings
- `summarize.go` - Stage 2: Generate summaries
- `sync.go` - Stage 3: Sync to Obsidian
- `normalize.go` - Stage 4: Tag normalization
- `state.go` - Sync state management
- `cache.go` - Local caching helpers
- `utils.go` - Utility functions

### Building

```bash
go build -o krisp-sync .
```

### Dependencies

- `github.com/joho/godotenv` - Environment variable loading
- `google.golang.org/genai` - Google Gemini AI client

## License

Private project - not for distribution.
