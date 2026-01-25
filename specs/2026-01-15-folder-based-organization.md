# Folder-Based Organization

**Date**: 2026-01-15
**Status**: Draft

## Overview

Replace the `space_id` concept with a folder-based organization system. This allows users to organize Notion pages into logical folders without requiring Notion workspace/database IDs.

## Motivation

The current `space_id` approach ties the organization to Notion's internal structure. A folder-based system provides:
- Simpler mental model for users
- Flexibility to organize pages by topic/team/project
- Decoupling from Notion's workspace structure
- Easier management of multiple page hierarchies

## Design

### Folder Structure

Pages are organized into folders within the `notion/` directory:

```
notion/
├── tech/
│   ├── architecture.md
│   ├── api-design.md
│   ├── architecture/
│   │   └── database-schema.md
│   └── api-design/
│       └── rest-endpoints.md
├── product/
│   ├── roadmap.md
│   └── roadmap/
│       └── q1-goals.md
└── default/
    └── welcome.md
```

**Folder naming rules**:
- Lowercase alphanumeric and hyphens only
- Default folder name: `default`
- No nested folders in the folder name itself (subfolders created by page hierarchy)

**Page naming**:
- Both root and child pages use the same naming convention: `$title.md`
- Format: `$title.md` where `$title` is the sanitized page title
- Example: Page titled "Architecture Overview" → `architecture-overview.md`
- **Important**: File names are stable - they do NOT change if the page is renamed in Notion

**File organization**:
- Root pages are placed directly in the folder directory: `$folder/$title.md`
- Child pages are nested under their parent: `$folder/$parent/$title.md`
- Example: Child of "Architecture Overview" → `architecture-overview/database.md`

**File name stability**:
- File names are determined when a page is first downloaded
- Renaming a page in Notion does NOT rename the local file
- This prevents git churn and maintains stable references
- The `title` field in state.json is updated to reflect the current Notion title
- To rename a file, manually rename it and update the path in state.json

### Command: `add`

Adds a root page to a folder and queues it for syncing.

**Syntax**:
```bash
./notion-sync add <page_id> [--folder|-f <folder_name>]
```

**Arguments**:
- `<page_id>`: Notion page ID or URL
- `--folder|-f`: Folder name (optional, defaults to "default")

**Behavior**:
1. Parse the page ID from argument (handle both raw IDs and URLs)
2. Determine folder name (use provided folder or "default")
3. Fetch the page from Notion API
4. Download page content to `$folder/$title.md`
5. Create queue entry of type `init` in `.notion-sync/queue/`
6. Store folder association in local state

**Example**:
```bash
# Add to 'tech' folder
./notion-sync add 1234567890abcdef --folder tech
# Creates: tech/page-title.md
# Queues: .notion-sync/queue/0001.json

# Add to default folder
./notion-sync add https://notion.so/My-Page-abc123
# Creates: default/my-page.md
```

**Exit codes**:
- `0`: Success
- `1`: Invalid page ID/URL
- `2`: Notion API error
- `3`: File system error

### Command: `sync`

Processes the queue and recursively syncs all pages.

**Syntax**:
```bash
./notion-sync sync [--folder|-f <folder_name>]
```

**Arguments**:
- `--folder|-f`: Only sync pages in specified folder (optional, defaults to all folders)

**Behavior**:
1. Load all queue files from `.notion-sync/queue/`
2. Filter by folder if specified
3. Process each queue entry:
   - Fetch page from Notion API
   - Download/update page content
   - Discover child pages
   - Queue child pages for processing (type `init`)
   - Mark current page as processed
4. Recursively process new queue entries until queue is empty
5. Clean up processed queue entries

**Queue processing rules**:
- Process queue files in numerical order (0001.json, 0002.json, etc.)
- Type `init`: Skip if page already exists locally and is up-to-date
- Type `update`: Always fetch and update, even if exists locally
- Child pages inherit parent's folder
- Track processed pages to avoid cycles

**Orphaned pages**:
- If a parent page is deleted in Notion, child pages remain in their current location
- Orphaned pages are not automatically deleted or moved
- Use `list` command to identify orphaned pages (parent_id points to non-existent page)

**Example**:
```bash
# Sync all folders
./notion-sync sync

# Sync only 'tech' folder
./notion-sync sync --folder tech
```

### Command: `list`

Lists all folders and their pages.

**Syntax**:
```bash
./notion-sync list [--folder|-f <folder_name>] [--tree|-t]
```

**Arguments**:
- `--folder|-f`: Only list pages in specified folder (optional, defaults to all folders)
- `--tree|-t`: Display as a tree structure showing parent-child relationships (optional)

**Behavior**:
1. Read state from `.notion-sync/state.json`
2. Filter by folder if specified
3. Display pages with metadata

**Default output format** (flat list):
```
tech (2 root pages, 5 total pages)
  _architecture-overview.md (last synced: 2h ago)
  _api-design.md (last synced: 1d ago)
  architecture-overview/database-schema.md (last synced: 2h ago)
  architecture-overview/backend.md (last synced: 2h ago)
  api-design/rest-endpoints.md (last synced: 1d ago)

product (1 root page, 3 total pages)
  _roadmap.md (last synced: 3h ago)
  roadmap/q1-goals.md (last synced: 3h ago)
  roadmap/q2-goals.md (last synced: 3h ago)
```

**Tree output format** (`--tree`):
```
tech (2 root pages, 5 total pages)
├── _architecture-overview.md (last synced: 2h ago)
│   ├── database-schema.md (last synced: 2h ago)
│   └── backend.md (last synced: 2h ago)
└── _api-design.md (last synced: 1d ago)
    └── rest-endpoints.md (last synced: 1d ago)

product (1 root page, 3 total pages)
└── _roadmap.md (last synced: 3h ago)
    ├── q1-goals.md (last synced: 3h ago)
    └── q2-goals.md (last synced: 3h ago)
```

**Orphaned pages indicator**:
```
tech (2 root pages, 5 total pages, 1 orphaned)
├── _architecture-overview.md (last synced: 2h ago)
└── orphaned-page.md (ORPHANED - parent deleted, last synced: 1w ago)
```

**Example**:
```bash
# List all folders and pages
./notion-sync list

# List only tech folder
./notion-sync list -f tech

# List with tree view
./notion-sync list --tree

# List specific folder as tree
./notion-sync list -f product --tree
```

### Command: `status`

Shows sync status and queue information.

**Syntax**:
```bash
./notion-sync status [--folder|-f <folder_name>]
```

**Arguments**:
- `--folder|-f`: Only show status for specified folder (optional, defaults to all folders)

**Behavior**:
1. Read queue files from `.notion-sync/queue/`
2. Read state from `.notion-sync/state.json`
3. Calculate and display statistics

**Output format**:
```
Notion Sync Status

Folders: 2 (tech, product)
Total pages: 8
Root pages: 3

Queue:
  Pending: 12 pages across 3 queue files
    - tech: 8 pages (5 init, 3 update)
    - product: 4 pages (4 init, 0 update)

Last sync:
  tech: 2 hours ago
  product: 3 hours ago

Next sync will process:
  - 0001.json: 5 pages (tech, init)
  - 0002.json: 3 pages (tech, update)
  - 0003.json: 4 pages (product, init)
```

**When filtered by folder**:
```bash
./notion-sync status -f tech
```

Output:
```
Notion Sync Status - tech folder

Pages: 5 (1 root page)
Queue: 8 pages pending (5 init, 3 update)
Last sync: 2 hours ago

Queue files:
  - 0001.json: 5 pages (init)
  - 0002.json: 3 pages (update)
```

**Empty queue**:
```
Notion Sync Status

Folders: 2 (tech, product)
Total pages: 8
Queue: empty
Last sync: 2 hours ago
```

**Example**:
```bash
# Show overall status
./notion-sync status

# Show status for tech folder
./notion-sync status -f tech
```

### Queue System

**Location**: `.notion-sync/queue/`

**File naming**: Sequential numbers with leading zeros: `0001.json`, `0002.json`, etc.

**Queue file format**:
```json
{
  "type": "init",
  "folder": "tech",
  "pageIds": [
    "1234567890abcdef",
    "fedcba0987654321"
  ]
}
```

**Queue file fields**:
- `type`: Either `"init"` or `"update"`
  - `"init"`: Don't re-process if page exists and is current
  - `"update"`: Always re-process, even if page exists
- `folder`: Folder name for these pages
- `pageIds`: Array of Notion page IDs to process

**Queue creation**:
- `add` command creates new queue file with next sequential number
- Each `add` creates a separate queue file
- No limit on number of queue files

**Queue lifecycle**:
1. Created by `add` command
2. Read by `sync` command
3. Processed entries are removed from queue
4. Empty queue files are deleted
5. New queue entries added for discovered child pages

**Deduplication**:
- Type `init`: If page ID already in any queue file, don't add again
- Type `update`: Always add, even if page ID exists in queues

### State Management

**Configuration changes**:
Remove `space_id` from all configuration:

```json
// OLD - Remove this
{
  "spaces": [
    {
      "space_id": "abc123",
      "root_page_id": "xyz789"
    }
  ]
}

// NEW - Replace with folders
{
  "folders": {
    "tech": {
      "root_pages": ["1234567890abcdef", "fedcba0987654321"]
    },
    "product": {
      "root_pages": ["aabbccddeeff0011"]
    }
  }
}
```

**State storage**: `.notion-sync/state.json`

```json
{
  "folders": {
    "tech": {
      "pages": {
        "1234567890abcdef": {
          "title": "Architecture Overview",
          "path": "tech/architecture-overview.md",
          "last_synced": "2026-01-15T10:30:00Z",
          "last_edited": "2026-01-14T15:20:00Z",
          "is_root": true,
          "parent_id": null,
          "children": ["child1", "child2"]
        },
        "child1": {
          "title": "Database Schema",
          "path": "tech/architecture-overview/database-schema.md",
          "last_synced": "2026-01-15T10:31:00Z",
          "last_edited": "2026-01-13T09:00:00Z",
          "is_root": false,
          "parent_id": "1234567890abcdef",
          "children": []
        }
      }
    }
  }
}
```

**State fields**:
- `title`: Current page title (updated if page is renamed in Notion)
- `path`: File system path relative to project root (NEVER changes after creation)
- `last_synced`: When we last fetched this page
- `last_edited`: Notion's last_edited_time for the page
- `is_root`: Whether this is a root page (has `_` prefix)
- `parent_id`: Parent page ID (null for root pages, may point to deleted page for orphans)
- `children`: Array of child page IDs

**Path stability**:
- The `path` field is set when the page is first downloaded
- The `path` NEVER changes automatically, even if the page is renamed in Notion
- This ensures stable file references and prevents git churn
- The `title` field tracks the current Notion title for display/search purposes
- Manual path changes require updating state.json directly

### Migration Path

**Removing space_id**:

1. **Database schema**: Remove all `space_id` columns/fields
2. **Configuration**: Replace `spaces` with `folders` structure
3. **Code**: Remove all space_id references
4. **File organization**: Keep existing files, use folder-based paths for new pages

**Backwards compatibility**: Not maintained. This is a breaking change requiring:
- Re-running `add` commands for existing root pages
- Re-syncing all content
- Updating any external scripts/automation

**Migration script** (optional):
```bash
# Hypothetical migration helper
./notion-sync migrate-to-folders
# Reads old config, prompts for folder names, creates new structure
```

## Examples

### Scenario 1: Adding tech documentation

```bash
# Add architecture doc to tech folder
./notion-sync add https://notion.so/Architecture-1234 -f tech

# Output:
# Downloaded: tech/architecture.md
# Queued: 1 page for sync

# Sync to get all children
./notion-sync sync -f tech

# Output:
# Processing queue: 1 pages
# Downloaded: tech/architecture/backend.md
# Downloaded: tech/architecture/frontend.md
# Downloaded: tech/architecture/backend/database.md
# Completed: 3 pages synced
```

### Scenario 2: Multiple root pages

```bash
# Add multiple root pages to product folder
./notion-sync add abc123 -f product  # Roadmap
./notion-sync add def456 -f product  # Features
./notion-sync add ghi789 -f product  # Metrics

# Sync all product pages
./notion-sync sync -f product
```

### Scenario 3: Default folder

```bash
# No folder specified - uses 'default'
./notion-sync add xyz999

# Creates: default/page-title.md
```

### Scenario 4: Force update

When you want to force re-download of a page:

```bash
# Add with update type (implementation detail - may need flag)
./notion-sync add abc123 -f tech --force-update

# Creates queue entry with type "update" instead of "init"
# Next sync will re-download even if page exists
```

### Scenario 5: File name stability on rename

When a page is renamed in Notion, the file name stays the same:

```bash
# Initial state
./notion-sync add page123 -f tech
# Downloads to: tech/architecture-overview.md
# State: {"title": "Architecture Overview", "path": "tech/architecture-overview.md"}

# User renames page in Notion to "System Architecture"
./notion-sync sync -f tech
# File stays: tech/architecture-overview.md
# State updated: {"title": "System Architecture", "path": "tech/architecture-overview.md"}

# List command shows current title
./notion-sync list -f tech
# Output: architecture-overview.md - "System Architecture" (last synced: 1m ago)
```

### Scenario 6: Orphaned pages

When a parent page is deleted in Notion:

```bash
# Initial hierarchy
tech/architecture-overview.md
tech/architecture-overview/database.md
tech/architecture-overview/api.md

# User deletes "Architecture Overview" page in Notion
./notion-sync sync -f tech

# Result:
tech/architecture-overview/database.md  # Still exists
tech/architecture-overview/api.md       # Still exists
# Note: architecture-overview.md is deleted

# State shows orphaned pages
./notion-sync list -f tech --tree
# Output:
# tech (0 root pages, 2 total pages, 2 orphaned)
# ├── database.md (ORPHANED - parent deleted, last synced: 1m ago)
# └── api.md (ORPHANED - parent deleted, last synced: 1m ago)
```

### Scenario 7: Using list and status commands

```bash
# Check what's in the system
./notion-sync list
# Shows all folders and pages

# Check sync status
./notion-sync status
# Output:
# Folders: 2 (tech, product)
# Total pages: 8
# Queue: 5 pages pending

# Add pages and check status again
./notion-sync add page1 -f tech
./notion-sync add page2 -f tech
./notion-sync status -f tech
# Output:
# Notion Sync Status - tech folder
# Pages: 5 (1 root page)
# Queue: 7 pages pending (5 init, 2 update)
# Last sync: 2 hours ago
```

## Implementation Notes

### File System Operations

- Create folders lazily (when first page is added)
- Use atomic writes for queue files
- Handle concurrent queue access (file locking or atomic renames)
- Clean up empty directories after page deletion

### Error Handling

- **Notion API errors**: Retry with exponential backoff
- **File system errors**: Fail fast, don't corrupt state
- **Invalid page IDs**: Validate before queuing
- **Duplicate adds**: Idempotent (no-op if already queued with type `init`)

### Performance Considerations

- Batch queue entries where possible
- Parallel page downloads (respect rate limits)
- Incremental sync (only changed pages)
- No queue file consolidation (unlimited queue files allowed)

### Logging

Use context-aware logging throughout:

```go
logger.InfoContext(ctx, "adding root page",
    "page_id", pageID,
    "folder", folder,
    "title", title)

logger.DebugContext(ctx, "processing queue entry",
    "queue_file", queueFile,
    "type", queueType,
    "page_count", len(pageIDs))
```

## Design Decisions

1. **Queue file limits**: No limit on queue files - unlimited queue files allowed
2. **Orphaned pages**: Pages stay in their current location when parent is deleted in Notion
3. **Rename handling**: File names are stable and do NOT change when page is renamed in Notion
4. **List command**: Added `./notion-sync list` with flat and tree views
5. **Status command**: Added `./notion-sync status` to show queue and sync statistics

## Testing Scenarios

### Basic Operations
1. Add root page to new folder
2. Add root page to existing folder
3. Add same page twice (should be idempotent)
4. Sync with deep nesting (5+ levels)
5. Sync with circular references (should handle gracefully)
6. Concurrent adds to different folders
7. Sync interruption and resume

### Page Lifecycle
8. Page deleted in Notion (should remove local file)
9. Page moved in Notion (should update local structure)
10. Parent page deleted in Notion (child becomes orphaned, stays in place)
11. Page renamed in Notion (file name stays same, title updated in state)
12. Root page renamed in Notion (file name with `_` prefix stays same)

### Queue Management
13. Multiple queue files for same folder
14. Queue files with both `init` and `update` types
15. Queue deduplication for `init` type entries
16. Queue allows duplicates for `update` type entries
17. Empty queue files are cleaned up after processing
18. Very large number of queue files (100+)

### Commands
19. `list` command with no pages
20. `list` command with multiple folders
21. `list --tree` with deep nesting
22. `list` command showing orphaned pages
23. `status` command with empty queue
24. `status` command with pending items
25. `status --folder` for specific folder

### Error Handling
26. Rate limit handling during bulk sync
27. Invalid page ID in `add` command
28. Network failure during sync
29. Notion API returns 404 for page (page deleted)
30. Corrupted state.json file

## Success Criteria

### Core Functionality
- ✅ `space_id` completely removed from codebase
- ✅ Folders work independently (can sync one without affecting others)
- ✅ Root and child pages use consistent naming convention
- ✅ Child pages correctly nested under parents
- ✅ Queue system handles `init` vs `update` types correctly
- ✅ Idempotent operations (can re-run safely)
- ✅ State tracking prevents unnecessary re-downloads
- ✅ Clean error messages for common failures

### File Stability
- ✅ File names remain stable when pages are renamed in Notion
- ✅ Title in state.json updated to reflect current Notion title
- ✅ Orphaned pages stay in place when parent is deleted

### Commands
- ✅ `add` command downloads page and creates queue entry
- ✅ `sync` command processes queue recursively
- ✅ `list` command shows all folders and pages (flat and tree views)
- ✅ `status` command shows queue and sync statistics
- ✅ All commands support `--folder` filter

### Queue System
- ✅ Unlimited queue files supported
- ✅ Queue files processed in numerical order
- ✅ Deduplication for `init` type entries
- ✅ No deduplication for `update` type entries
- ✅ Empty queue files cleaned up automatically
