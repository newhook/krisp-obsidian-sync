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
- **Automatic timezone conversion**: Converts meeting times from UTC to your local timezone
- **Selective field updates**: Update only specific frontmatter fields without losing manual edits
- **Graceful interruption**: Supports Ctrl+C cancellation with state preservation
- **Resumable**: State tracking allows you to resume interrupted operations

## Prerequisites

1. **Krisp.ai account** with API access
2. **Google Cloud project** with Vertex AI API enabled
3. **Obsidian vault** (local directory)
4. **Go 1.24.2+** installed

## Setup

1. Create a `.env` file in the project directory:

```env
KRISP_BEARER_TOKEN=your_krisp_api_token
GOOGLE_CLOUD_PROJECT=your-gcp-project-id
GOOGLE_CLOUD_LOCATION=us-central1
OBSIDIAN_VAULT_PATH=/path/to/your/Obsidian Vault
```

2. Build the project:

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
  - `all` - Run all stages in sequence (extract-tags, download, summarize, sync)
  - `download` - Download meetings from Krisp API to local cache
  - `summarize` - Generate AI summaries for cached meetings
  - `sync` - Sync cached meetings and summaries to Obsidian
  - `check-updates` - Check Krisp API for updated meetings and sync changes to Obsidian
  - `extract-tags` - Extract all existing tags from Obsidian vault to obsidian-tags.json
  - `normalize-prompt` - Generate tag normalization prompt for initial mass import
  - `repair` - Sync filesystem state with tracking state

- `--limit <n>` - Number of meetings to process (default: `1` for testing)
  - Set to `0` to process all available meetings
  - Useful for testing with small batches first

- `--overwrite` - Force re-process meetings, ignoring state
  - When used alone: Re-processes ALL meetings (clears all state)
  - When used with `--meeting`: Re-processes only that specific meeting
  - Re-summarizes meetings (ignoring summarization state)
  - Re-syncs meetings to Obsidian (ignoring sync state)
  - Useful if you've modified templates or prompts

- `--test` - Test mode for sync stage only
  - Creates a single test file without updating state
  - Processes the oldest meeting
  - Can be run repeatedly for testing templates
  - Does not mark meetings as synced

- `--meeting <meeting-id>` - Process specific meeting(s) by ID
  - Supports comma-separated IDs: `--meeting id1,id2,id3`
  - Combine with `--overwrite` to re-summarize and re-sync
  - Useful for fixing issues with individual meetings or batches

- `--apply-normalization` - Apply tag normalization during sync (for initial mass import only)
  - Loads `normalize-result.json` and `normalize-premappings.json`
  - Applies tag mappings when writing to Obsidian
  - Use only during initial mass import, not for daily incremental syncs

- `--update-fields <field1,field2,...>` - Update only specific frontmatter fields in existing Obsidian files
  - Reads existing files and preserves all fields except those specified
  - Useful for fixing issues without losing manual edits (tags, participants, etc.)
  - Case-insensitive field matching
  - Example: `--update-fields time,date` updates only time and date fields
  - Only processes existing files (skips files that don't exist)


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
- Automatically loads existing tags from Obsidian vault (obsidian-tags.json) to guide tag suggestions
- Uses meeting transcripts to generate:
  - One-line description (max 10 words)
  - Relevant tags (preferring existing Obsidian tags when appropriate)
  - List of topics discussed
  - Detailed summaries for each topic
- Saves summaries to `meetings/<meeting-id>-summary.json`
- Tracks summarized meetings in state file

### Stage 3: Sync

Syncs meetings and summaries to your Obsidian vault. All timestamps are automatically converted from UTC to your local timezone.

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

## Common Workflows

### First-time sync of all meetings

```bash
# Download all meetings
./krisp-sync --step download --limit 0

# Summarize all meetings
./krisp-sync --step summarize --limit 0

# Sync to Obsidian
./krisp-sync --step sync --limit 0
```

After the first sync, you may want to normalize tags (see below).

### Incremental sync (daily use)

```bash
# Run all stages for new meetings only
# This automatically extracts Obsidian tags for AI summarization
./krisp-sync --limit 0
```

This workflow:
1. Extracts tags from your Obsidian vault (obsidian-tags.json)
2. Downloads new meetings from Krisp
3. Generates AI summaries using existing Obsidian tags
4. Syncs to Obsidian vault

### Testing with small batches

```bash
# Process just 5 meetings to test
./krisp-sync --limit 5
```

### Re-sync to Obsidian after template changes

```bash
./krisp-sync --step sync --overwrite --limit 0
```

### Re-generate summaries after prompt changes

```bash
./krisp-sync --step summarize --overwrite --limit 0
```

### Re-process a single meeting that had issues

```bash
# Re-summarize AND re-sync a specific meeting
./krisp-sync --meeting fd00fb02629c46d0981c968a5565ecc6 --overwrite

# Or run specific stages separately
./krisp-sync --step summarize --meeting fd00fb02629c46d0981c968a5565ecc6 --overwrite
./krisp-sync --step sync --meeting fd00fb02629c46d0981c968a5565ecc6 --overwrite
```

### Test workflow with single meeting

```bash
# Summarize one meeting
./krisp-sync --step summarize --limit 1

# Test sync output
./krisp-sync --step sync --test
```

### Update specific fields in existing meetings

If you need to update only certain frontmatter fields without losing your manual edits (e.g., after fixing a timezone bug or updating templates):

```bash
# Update only time and date fields in all meetings
./krisp-sync --step sync --limit 0 --update-fields time,date

# Update only description field
./krisp-sync --step sync --limit 0 --update-fields description

# Update specific meetings
./krisp-sync --step sync --meeting id1,id2,id3 --update-fields time,date

# Update multiple fields
./krisp-sync --step sync --limit 0 --update-fields time,date,description
```

This preserves:
- All manually added or modified tags
- Custom participant names
- Any other frontmatter fields not specified
- The entire body content of the note

**Use case**: If you've manually curated tags or fixed participant names, use `--update-fields` instead of `--overwrite` to avoid losing those edits.

### Automatically sync changes from Krisp.ai

If you've fixed participant names, updated meeting titles, or made other changes in Krisp.ai, you can automatically detect and sync those changes:

```bash
# Check for updates and automatically sync changed fields to Obsidian
./krisp-sync --step check-updates
```

This will:
1. Efficiently fetch all meetings from Krisp API in a single request
2. Compare with local cache to detect changes (participants, title, duration)
3. Show what changed for each meeting
4. Update only the changed metadata in local cache (transcript data is preserved)
5. Automatically sync only the changed fields to Obsidian (preserving your manual edits)

**Example output:**
```
🔍 Checking for updated meetings on Krisp API...
📊 Total meetings on Krisp: 1504
📦 Cached meetings: 1504

🔎 Comparing cached meetings with API...
  Checked 100/1504 meetings...
  🔄 0199eea7f41970609f799e8439462a3e has changes:
     - participants: 'Matthew Newhook' → 'Matthew Newhook, Michael Whelan'

✅ Found and updated 1 meeting(s) with changes

📝 Syncing changes to Obsidian...
  Updating fields [participants] for 1 meeting(s)...
    ✓ Updated 0199eea7f41970609f799e8439462a3e

✅ All changes synced to Obsidian!
```

### Tag normalization for initial mass import (optional)

If you've already imported many meetings before starting to use krisp-sync, you may want to consolidate similar tags for consistency. This is a **one-time workflow** for initial mass imports only. Daily incremental syncs automatically use your existing Obsidian tags.

#### Step 1: Generate normalization prompt

```bash
./krisp-sync --step normalize-prompt
```

This analyzes all meeting summaries and:
- Performs fuzzy matching to pre-consolidate obvious duplicates (e.g., "product-roadmap" and "product-road-map")
- Generates `normalize-prompt-generated.txt` - a prompt to send to your LLM
- Generates `normalize-premappings.json` - fuzzy pre-consolidated tag mappings

#### Step 2: Process with your LLM (manual)

1. Copy the contents of `normalize-prompt-generated.txt`
2. Send it to your preferred LLM (Claude, Gemini, GPT-4, etc.)
3. The LLM will consolidate semantically related tags (e.g., "product-strategy", "product-roadmap", "product-vision" → "product-strategy")
4. Save the LLM's JSON response to `normalize-result.json` in this format:

```json
[
  {
    "canonical_tag": "product-strategy",
    "old_tags": ["product-strategy", "product-roadmap", "product-vision"]
  },
  {
    "canonical_tag": "engineering",
    "old_tags": ["engineering", "technical-discussion", "architecture"]
  }
]
```

#### Step 3: Re-sync meetings with normalized tags

```bash
./krisp-sync --step sync --apply-normalization --overwrite --limit 0
```

This will:
- Load `normalize-result.json` and `normalize-premappings.json`
- Merge the mappings (premappings → LLM mappings)
- Apply tag consolidation when writing to Obsidian
- Overwrite all meeting files with normalized tags

**Note**: Meeting summary JSON files remain unchanged - normalization is applied only when writing to Obsidian.

#### Future syncs

After the initial import, **do not use `--apply-normalization`** for daily incremental syncs. The default workflow automatically uses tags from your Obsidian vault to guide AI summarization, ensuring consistency without manual normalization.

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

## Troubleshooting

### "No cached meetings found"

Run the download stage first:
```bash
./krisp-sync --step download --limit 10
```

### "All meetings already synced"

Either all meetings are up to date, or use `--overwrite` to force re-sync:
```bash
./krisp-sync --step sync --overwrite
```

### Meeting times are incorrect (timezone issue)

All meeting times are automatically converted from UTC to your local timezone. If you have existing meetings with incorrect times:

```bash
# Update only time and date fields, preserving all manual edits
./krisp-sync --step sync --limit 0 --update-fields time,date
```

This will fix the timestamps without losing any manually added tags or other edits.

### Ctrl+C during operation

The state is saved after each meeting is processed, so you can safely resume where you left off.

### Rate limiting / API errors

If you encounter rate limits from the Krisp API, the tool will show an error. You can try running smaller batches with `--limit` or waiting before retrying.

**Note**: The `--check-updates` feature is optimized to use a single API call to fetch meeting metadata for comparison. For large collections (1500+ meetings), it typically completes in under 30 seconds. Changed metadata is updated in-place in the cache - full meeting data (transcripts) is never re-downloaded.

### Want to update files without losing manual edits

Use `--update-fields` instead of `--overwrite`:

```bash
# Update only specific fields
./krisp-sync --step sync --update-fields description,tags --limit 0
```

This preserves any fields you haven't specified, including manual edits.

## Development

### Project Structure

- `main.go` - Entry point, CLI parsing, embedded templates
- `krisp.go` - Krisp API client
- `download.go` - Stage 1: Download meetings
- `summarize.go` - Stage 2: Generate summaries
- `sync.go` - Stage 3: Sync to Obsidian
- `check-updates.go` - Check for updated meetings and auto-sync changes
- `normalize.go` - Tag normalization workflow
- `state.go` - Sync state management
- `cache.go` - Local caching helpers
- `utils.go` - Utility functions

### Building

```bash
go build -o krisp-sync .
```

### Dependencies

- `github.com/joho/godotenv` - Environment variable loading from .env files
- `github.com/lithammer/fuzzysearch` - Fuzzy string matching for tag pre-consolidation
- `github.com/yuin/goldmark` - Markdown parser for extracting tags from Obsidian notes
- `google.golang.org/genai` - Google Gemini AI client (Vertex AI)
- `gopkg.in/yaml.v3` - YAML parser for Obsidian frontmatter

## License

MIT License - see [LICENSE](LICENSE) file for details.
