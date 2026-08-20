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
    └── ids/                        # Sharded registry index
        ├── manifest.json           # ntnsync_version + schema_version, once
        ├── page/
        │   ├── 00.jsonl            # 256 shards, keyed by the first two hex
        │   └── ...                 # characters of the normalized page ID
        ├── file/
        │   ├── 00.jsonl
        │   └── ...
        └── user.jsonl              # too few users to be worth sharding
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

## Registry Index

**Path**: `.notion-sync/ids/`

The registry index tracks metadata for every synced page, downloaded file and
cached user. It is stored as **sharded JSON Lines**, not as one file per record.

```
.notion-sync/ids/
  manifest.json      {"ntnsync_version": "...", "schema_version": 1}
  page/00.jsonl … page/ff.jsonl      256 shards
  file/00.jsonl … file/ff.jsonl      256 shards
  user.jsonl                          single file
```

| Property | Rule |
|----------|------|
| Shard key | First two hex characters of the normalized (dash-less) ID |
| Format | JSON Lines — one compact record per line |
| Key order | Sorted alphabetically inside each record |
| Record order | Sorted by `id` inside each shard |
| Version | `ntnsync_version` lives once in `manifest.json`, never per record |

> **Why sharded.** Git rewrites a directory's whole tree object whenever any file
> inside it changes. A flat `ids/` directory with 41,000 entries emitted a fresh
> ~2.9 MB tree object on *every* sync commit, which dominated both the pack size
> and the memory used to decode trees during clone/checkout. 256 shards keep the
> tree trivial while each shard stays small enough to delta-compress well. The
> deterministic ordering is what makes a one-record change a one-line diff.

**Writes are batched**: records are buffered in memory and every affected shard is
written exactly once per commit cycle, so a sync touching 50 pages does not
rewrite the same shard 50 times.

### Migration from the per-file layout

Older repositories stored one file per record
(`.notion-sync/ids/page-{id}.json`). Reads still fall back to those locations, so
an un-migrated repository keeps working. Run `ntnsync reindex --compact` once to
convert every legacy file into the shards and delete the old files in a single
commit.

## Page Registries

**Path**: `.notion-sync/ids/page/{shard}.jsonl`

Page records track metadata for each synced page. The `id` field is normalized (no dashes).

> **Why normalization matters.** Notion's REST API and webhook events deliver IDs
> in the dashed UUID form (`388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64`); everything in
> ntnsync keys on the dash-less form (`388aa28b3ffb80b69e5bc6a0eeaebf64`). IDs are
> therefore normalized at every entry point (`notion.NormalizeID`), including the
> webhook handler. If a registry is ever written under the dashed form, the same
> page fails its file-path stability check and the conflict resolver no longer
> recognizes it as itself — so it is written to a second, suffixed file (see
> *Filename Conflicts*). Reads (`loadPageRegistry`) fall back to the dashed form
> for backward compatibility, and a normal re-sync rewrites the entry into the
> shard under the normalized ID and removes the stale dashed file.

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

**Path**: `.notion-sync/ids/file/{shard}.jsonl`

Tracks downloaded files (images, PDFs, etc.) to avoid re-downloading.

```json
{
  "id": "abc123...",
  "file_path": "tech/wiki/images/diagram.png",
  "source_url": "https://s3.amazonaws.com/notion-user-content/...",
  "last_synced": "2026-01-18T18:05:06Z"
}
```

`source_url` keeps only the bare object path. Notion hands out pre-signed S3 URLs
whose query string carries ~2 KB of credentials, signature and expiry that are
never read back and rotate on every fetch, so everything from `?` onwards is
stripped before the record is written.

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

### Optional Separate Queue Branch

When `NTN_QUEUE_BRANCH` is set, only `.notion-sync/queue/` is committed to that
branch; everything else — page content, `.notion-sync/ids/` and
`.notion-sync/state.json` — stays on the main branch (`NTN_GIT_BRANCH`).

| Path | Branch |
|------|--------|
| Page content (`tech/…`, `root.md`, …) | main |
| `.notion-sync/ids/` (shards + manifest) | main |
| `.notion-sync/state.json` | main |
| `.notion-sync/queue/` | queue branch |

This isolates the high-frequency "queued page" commits on a dedicated branch so
the main branch history contains only meaningful content and registry changes.
Internally the queue branch is checked out into a sibling working directory
(`<store-path>-queue`) so the main working tree never contains — and therefore
never commits — the queue checkout. The queue branch is created automatically if
it does not yet exist on the remote.

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

## Filename Conflicts

When two **different** pages would sanitize to the same filename in the same
directory, the second one gets a 4-character suffix derived from its page ID,
e.g. `comite-strategique.md` and `comite-strategique-388a.md`. The suffix is the
first characters of the page's own (normalized) ID.

A page must never collide with **itself**: the conflict resolver skips any
registry whose normalized ID equals the page's own. Two files sharing the same
`notion_id` are therefore always a bug, not legitimate disambiguation — historically
caused by un-normalized (dashed) registry IDs. Run `reindex` to detect and merge
such duplicates (it keeps the most recently edited file).

## Orphaned Pages

If a parent page is deleted in Notion:
- Child pages remain in place
- Marked as orphaned in registry
- Still accessible but without parent context
- `list` command shows orphaned status
