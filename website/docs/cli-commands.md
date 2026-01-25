---
sidebar_position: 2
---

# CLI Commands

ntnsync provides commands for syncing Notion content to a local git repository.

## Global Flags

| Flag | Env Var | Description |
|------|---------|-------------|
| `--token` | `NOTION_TOKEN` | Notion API token (required) |
| `--store-path`, `-s` | `NTN_DIR` | Git repository path (default: `notion`) |
| `--verbose` | | Enable debug logging |

## Logging Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_LOG_FORMAT` | `text` | Log output format: `text` (human-readable) or `json` (structured) |

**`NTN_LOG_FORMAT`**: Controls log output format.
- `text` (default): Human-readable text format suitable for development
- `json`: JSON-formatted structured logs for CI/CD, log aggregation, and monitoring

**Examples**:
```bash
# Default text format
./ntnsync sync -v

# JSON format for CI/CD pipelines
NTN_LOG_FORMAT=json ./ntnsync sync -v
```

**JSON output example**:
```json
{"time":"2026-01-24T10:30:45Z","level":"INFO","msg":"Starting sync"}
{"time":"2026-01-24T10:30:46Z","level":"DEBUG","msg":"Processing page","page_id":"abc123"}
```

## Commit/Push Environment Variables

Git commit and push behavior is controlled via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_COMMIT` | `false` | Enable automatic git commit after changes |
| `NTN_COMMIT_PERIOD` | `0` | Commit periodically during sync (e.g., `30s`, `1m`, `5m`) |
| `NTN_PUSH` | auto | Push to remote after commits |

**`NTN_COMMIT`**: Set to `true`, `1`, or `yes` to enable commits.

**`NTN_COMMIT_PERIOD`**: When set to a duration (e.g., `1m`), commits are made periodically during long sync operations. This also implicitly enables `NTN_COMMIT`.

**`NTN_PUSH`**: Controls whether to push after commits.
- Defaults to `true` when `NTN_GIT_URL` is set (remote mode)
- Defaults to `false` when `NTN_GIT_URL` is not set (local mode)
- Can be explicitly set to `true` to push to local repo's configured remote
- Set to `false` to commit locally without pushing

**Examples**:
```bash
# Commit and push (when NTN_GIT_URL is set)
NTN_COMMIT=true ./ntnsync sync

# Commit but don't push
NTN_COMMIT=true NTN_PUSH=false ./ntnsync sync

# Periodic commits during long sync
NTN_COMMIT_PERIOD=1m ./ntnsync sync
```

## Commands

### add

Add a root page to a folder.

```bash
ntnsync add <page_id_or_url> [--folder FOLDER] [--force-update]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | `default` | Target folder name |
| `--force-update` | false | Force re-download even if exists |

**Behavior**:
- Downloads page to `$folder/$title.md`
- Sets `is_root: true` in registry
- Queues child pages for recursive sync
- Accepts Notion URLs or raw page IDs
- Auto-detects and handles databases

**Examples**:
```bash
ntnsync add https://www.notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d --folder tech
ntnsync add 2c536f5e48f44234ad8d73a1a148e95d -f product
ntnsync add https://www.notion.so/My-Database --force-update
```

### get

Fetch a single page without marking it as root.

```bash
ntnsync get <page_id_or_url> [--folder FOLDER]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | auto-detect | Target folder (optional) |

**Behavior**:
- Fetches single page with `is_root: false`
- Auto-detects folder by tracing parent chain to existing root
- Fetches missing parent pages recursively
- Places page in correct hierarchy location
- Queues child pages

**Use cases**:
- Fetch specific page deep in hierarchy
- Recover a deleted page
- Add page that's part of existing tree

### scan

Re-scan a page to discover all children.

```bash
ntnsync scan <page_id_or_url>
```

**Behavior**:
- Re-scans existing page for all child pages
- Finds pages not yet tracked locally
- Queues new children with type `init`
- Reports statistics (total, new, already tracked)

**Use cases**:
- Discover pages added after initial sync
- Re-scan after reorganizing in Notion
- Ensure all descendants are tracked

### pull

Queue changed pages for syncing.

```bash
ntnsync pull [options]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | all | Filter to specific folder |
| `--since`, `-s` | last pull | Time override (e.g., `24h`, `7d`, `30d`) |
| `--max-pages`, `-n` | 0 | Limit pages queued (0 = unlimited) |
| `--all` | false | Include undiscovered pages |
| `--dry-run` | false | Preview without modifying |
| `--verbose` | false | Detailed logging |

**Behavior**:
- Fetches pages changed since last pull
- Queues them with type `update` and timestamps
- Stores `last_pull_time` in state.json
- Default mode: checks only tracked pages
- `--all` mode: discovers new accessible pages
- Stops early when reaching `oldest_pull_result`

**Note**: First pull requires `--since` flag (no previous pull time).

**Examples**:
```bash
ntnsync pull --since 24h --folder tech
ntnsync pull --all --max-pages 100 --dry-run
ntnsync pull -s 7d -n 500
```

### sync

Process the queue and download pages.

```bash
ntnsync sync [options]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | all | Sync only specific folder |
| `--max-pages`, `-n` | 0 | Max pages to process (0 = unlimited) |
| `--max-files`, `-w` | 0 | Max markdown files to write (0 = unlimited) |
| `--max-time`, `-t` | 0 | Duration limit (e.g., `30s`, `5m`, `1h`) |
| `--stop-after` | | Alias for `--max-time` |
| `--max-queue-files`, `-q` | 0 | Max queue files to process |

**Behavior**:
- Processes queue entries in `.notion-sync/queue/`
- Downloads pages recursively
- Fetches parents first for proper structure
- Type `init`: skips if exists and current
- Type `update`: compares timestamps, skips unchanged
- Remaining queue entries stay for next sync
- Creates git commit if `NTN_COMMIT=true`
- Commits periodically if `NTN_COMMIT_PERIOD` is set

**Examples**:
```bash
ntnsync sync --max-pages 100
ntnsync sync --folder tech -t 10m
NTN_COMMIT=true ntnsync sync -n 50 -w 20
NTN_COMMIT_PERIOD=1m ntnsync sync  # Periodic commits during long sync
```

### list

List folders and pages.

```bash
ntnsync list [--folder FOLDER] [--tree]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | all | List specific folder only |
| `--tree` | false | Show hierarchical structure |

**Output**:
- Lists all folders and their pages
- Shows root page count, total pages, orphaned count
- `--tree` shows parent-child hierarchy

### status

Show sync status and queue statistics.

```bash
ntnsync status [--folder FOLDER]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--folder`, `-f` | all | Show specific folder status |

**Output**:
- Folder and page counts
- Last sync time
- Queue statistics (pending pages by type and folder)
- Queue file details

### reindex

Rebuild registry files from markdown files.

```bash
ntnsync reindex [--dry-run]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | false | Preview changes without modifying |

**Behavior**:
- Scans all markdown files recursively
- Parses frontmatter for metadata
- Rebuilds `.notion-sync/ids/page-{id}.json` registries
- Handles duplicates by keeping latest `last_edited`
- Deletes older duplicate files
- Normalizes page IDs

**Use cases**:
- Recover from deleted registry files
- Fix corrupted registry data
- Clean up duplicate pages
- Rebuild after manual file edits

## Typical Workflows

### First-time sync

```bash
# 1. Add a root page
ntnsync add https://www.notion.so/Wiki-2c536f5e... --folder tech

# 2. Sync the queue (with commit)
NTN_COMMIT=true ntnsync sync --folder tech
```

### Incremental updates

```bash
# 1. Pull changes since last sync
ntnsync pull --folder tech

# 2. Sync the queue (with commit and push)
NTN_COMMIT=true ntnsync sync --folder tech
```

### Add specific page to existing tree

```bash
# Auto-detects folder from parent chain
ntnsync get https://www.notion.so/SpecificPage-abc123

# Sync the page and its children (with commit)
NTN_COMMIT=true ntnsync sync
```

### CI/CD automated sync

```bash
# Set environment variables for automated operation
export NTN_COMMIT=true
export NTN_GIT_URL=https://github.com/user/docs.git
export NTN_GIT_PASS=$GITHUB_TOKEN

# Pull and sync - commits and pushes automatically
ntnsync pull --since 2h
ntnsync sync
```
