---
sidebar_position: 3
---

# File Architecture

ntnsync uses a folder-based organization system to store synced Notion pages as markdown files, with metadata stored in the `.notion-sync/` directory.

## Directory Structure

```
{store-path}/
├── tech/                            # User-defined folder
│   ├── wiki.md                      # Root page
│   └── wiki/                        # Child pages directory
│       ├── architecture.md
│       └── architecture/
│           └── database-schema.md
├── product/                         # Another folder
│   └── roadmap.md
├── default/                         # Default folder
│   └── welcome.md
└── .notion-sync/                    # Metadata directory
    ├── state.json                   # Global state
    ├── queue/                       # Pending sync queue
    │   ├── 00000001.json
    │   └── 00000002.json
    └── ids/                         # Page registries
        ├── page-{id}.json
        └── file-{id}.json
```

## Folders

Folders are logical organization units for grouping related pages.

- **Naming**: Lowercase alphanumeric and hyphens only (`[a-z][a-z0-9-]+`)
- **Default folder**: `default` (used when no folder specified)
- **Root pages**: Stored directly in folder directory (`{folder}/{title}.md`)
- **Child pages**: Stored in subdirectories under parent (`{folder}/{parent}/{child}.md`)

## State File

**Path**: `.notion-sync/state.json`

```json
{
  "version": 3,
  "folders": ["tech", "product", "default"],
  "last_pull_time": "2026-01-23T10:30:00Z",
  "oldest_pull_result": "2026-01-20T15:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `version` | int | Schema version (currently 3) |
| `folders` | []string | List of folder names in use |
| `last_pull_time` | timestamp | When `pull` command last completed (optional) |
| `oldest_pull_result` | timestamp | Oldest page seen in last pull for early stopping (optional) |

## Page Registries

**Path**: `.notion-sync/ids/page-{id}.json`

Registry files track metadata for each synced page. The ID in the filename is normalized (no dashes).

```json
{
  "id": "2c536f5e48f44234ad8d73a1a148e95d",
  "type": "page",
  "folder": "tech",
  "file_path": "tech/wiki/architecture.md",
  "title": "Architecture",
  "last_edited": "2025-12-10T13:39:00Z",
  "last_synced": "2026-01-18T18:05:06.855833+01:00",
  "is_root": false,
  "parent_id": "abc123def456",
  "children": ["child1id", "child2id"],
  "content_hash": "sha256hash..."
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Notion page ID (normalized, no dashes) |
| `type` | string | `"page"` or `"database"` |
| `folder` | string | Folder name where page is stored |
| `file_path` | string | Relative path to markdown file |
| `title` | string | Page title (can change; file path doesn't) |
| `last_edited` | timestamp | Last edited time from Notion API |
| `last_synced` | timestamp | When we last synced this page |
| `is_root` | boolean | Whether this is a root page |
| `parent_id` | string | Parent page/database ID (empty for root pages) |
| `children` | []string | List of direct child page IDs |
| `content_hash` | string | SHA256 hash for change detection |

## File Registries

**Path**: `.notion-sync/ids/file-{id}.json`

Tracks downloaded files (images, PDFs, etc.) to avoid re-downloading.

```json
{
  "id": "abc123...",
  "file_path": "tech/wiki/images/diagram.png",
  "source_url": "https://s3.amazonaws.com/notion-user-content/...",
  "last_synced": "2026-01-18T18:05:06Z"
}
```

## Queue System

**Path**: `.notion-sync/queue/00000001.json`, `00000002.json`, etc.

Queue files hold pages waiting to be synced. Files are processed in order and deleted after processing.

### New Format (with timestamps)

```json
{
  "type": "update",
  "folder": "tech",
  "pages": [
    {
      "id": "24caa28b3ffb8009a1b0c5136acc373e",
      "last_edited": "2025-10-08T06:33:00Z"
    }
  ],
  "parentId": "2c536f5e48f44234ad8d73a1a148e95d",
  "createdAt": "2026-01-18T18:05:06.915087+01:00"
}
```

### Legacy Format (still supported)

```json
{
  "type": "init",
  "folder": "tech",
  "pageIds": ["id1", "id2", "id3"],
  "parentId": "parent_id",
  "createdAt": "2026-01-18T18:05:06Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `"init"` (skip if exists) or `"update"` (always process) |
| `folder` | string | Target folder for pages |
| `pages` | []object | Array with `{id, last_edited}` pairs (new format) |
| `pageIds` | []string | Plain array of page IDs (legacy format) |
| `parentId` | string | Parent page/database ID for child pages |
| `createdAt` | timestamp | When queue entry was created |

**Limits**:
- Maximum 10 pages per queue file
- Large batches are split across multiple files
- Sequential numbering ensures FIFO processing

## File Path Stability

File paths **never change** when pages are renamed in Notion:
- Original filename derived from title at first sync
- Registry `title` field updates on rename
- `file_path` remains constant
- Ensures stable git history and external references

## Filename Sanitization

Filenames follow the pattern `[a-z][a-z0-9-]+`:

| Rule | Example |
|------|---------|
| Must start with a letter | `123-page` → `page` |
| Lowercase only | `ISO 27001` → `iso-27001` |
| Only letters, numbers, hyphens | `Page (Main)` → `page-main` |
| Non-ASCII removed | `Présentations` → `prsentations` |
| Separators become hyphens | `DB::Table` → `db-table` |
| Max 100 characters | Truncated if longer |

## Orphaned Pages

If a parent page is deleted in Notion:
- Child pages remain in place
- Marked as orphaned in registry
- Still accessible but without parent context
- `list` command shows orphaned status
