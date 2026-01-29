# Root Manifest File (root.md)

**Date:** 2026-01-29
**Status:** Draft

## Problem

Currently, there's no way to:
1. See all root pages in a single, human-readable file
2. Temporarily disable syncing of a root page and its children without deleting it
3. Have a centralized configuration for which roots are active

## Breaking Changes

The `add` command is removed. Root pages are now managed by editing `root.md` directly.

## Terminology

The column controlling whether a root is synced is named `enabled`. This term was chosen over alternatives like `indexed`, `sync`, or `active` because it's clear, follows standard configuration patterns, and is self-documenting.

## Proposed Solution

### Root Manifest File

Create a `root.md` file at the repository root (next to `.notion-sync/`) containing a markdown table of all root pages:

```markdown
# Root Pages

| folder | enabled | url |
|--------|---------|-----|
| tech | [x] | https://notion.so/abc123 |
| product | [x] | https://notion.so/def456 |
| archive | [ ] | https://notion.so/xyz789 |
```

### Column Definitions

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `folder` | string | `default` | Target folder for this root page |
| `enabled` | checkbox | `[x]` (checked) | Whether to sync this root and its children |
| `url` | URL | (required) | Notion page URL (used to derive page ID) |

### Startup Reconciliation

On startup (before any command executes), the program:

1. **Reads `root.md`** and parses all entries
2. **Removes duplicates**: If multiple rows have the same page ID (extracted from URL), keep only the first occurrence and rewrite `root.md`
3. **Syncs registries**: For each root in `root.md`:
   - If `ids/page-{pageid}.json` exists, update its `Enabled` and `Folder` fields to match `root.md`
   - If registry doesn't exist, create it with `IsRoot: true` and values from `root.md`
4. **Orphans registries with `IsRoot: true`** that are not in `root.md` are left for `cleanup` command

### Behavior Changes

#### 1. Adding Root Pages

Users add roots by editing `root.md` directly:

```markdown
| folder | enabled | url |
|--------|---------|-----|
| tech | [x] | https://notion.so/abc123 |
| newroot | [x] | https://notion.so/newpage456 |  <!-- Added manually -->
```

On next startup, the registry is created automatically.

#### 2. Pull Operation

When `ntnsync pull` is called:
- Read `root.md` to get the list of enabled roots
- Only query and queue pages that belong to enabled roots
- Skip pages whose root has `enabled` unchecked

#### 3. Sync/Process Operation

When `ntnsync sync` is called:
- Before processing any page, verify it traces back to a root with `enabled: true`
- Skip pages (and log) if their root is disabled
- This prevents orphaned children from being synced

#### 4. Page Creation Check

Before saving any page (root or child):
1. Trace the page's ancestry to find its root page
2. Check that the root has:
   - `is_root: true` in its registry
   - `enabled: [x]` in `root.md`
3. If either condition fails, skip the page

### Registry Changes

Add an `Enabled` field to `PageRegistry` for root pages:

```go
type PageRegistry struct {
    // ... existing fields ...
    Enabled bool `json:"enabled,omitempty"` // Only meaningful for root pages
}
```

This provides fast lookup during sync (registry is synced from `root.md` on startup).

### File Format Details

The `root.md` file uses standard GitHub-flavored markdown:
- Checkboxes: `[x]` for enabled, `[ ]` for disabled
- URLs are stored as provided, but compared by page ID only
- File is human-editable
- Page IDs are extracted and normalized for all comparisons

### Commands

#### Cleanup orphaned pages

```bash
./ntnsync cleanup
```

- Finds all pages in the registry that don't trace back to a root listed in `root.md`
- Deletes the registry entries for orphaned pages
- Deletes the synced markdown files for orphaned pages
- Use `--dry-run` to preview what would be deleted without making changes

This is useful after:
- Removing a root from `root.md`
- Cleaning up after webhook-discovered pages whose root was never added

## Implementation Notes

### Initialization

- If `root.md` doesn't exist, create it as an empty file with just the header
- New roots discovered via webhooks are automatically added to `root.md`

### Empty root.md Template

When created, `root.md` starts with:

```markdown
# Root Pages

| folder | enabled | url |
|--------|---------|-----|
```

### Page ID Normalization

All URL comparisons are done by page ID only:
1. Extract page ID from URL (handles various Notion URL formats)
2. Normalize ID: remove dashes, lowercase
3. Compare normalized IDs, not full URLs

This prevents duplicates like:
- `https://notion.so/My-Page-abc123def456`
- `https://notion.so/abc123def456`
- `abc123-def456`

All resolve to the same page ID `abc123def456`.

### Parsing root.md

Use a simple markdown table parser:
1. Find table header with expected columns
2. Parse each row, extracting folder, checkbox state, and URL
3. Normalize page IDs from URLs for comparison and storage

### Startup Sync Algorithm

```
function reconcileRootMd():
    roots = parseRootMd()
    seenIds = {}
    cleanedRoots = []

    for root in roots:
        pageId = extractPageId(root.url)
        if pageId in seenIds:
            log.warn("Duplicate root removed", pageId, root.url)
            continue
        seenIds[pageId] = true
        cleanedRoots.append(root)

        registry = loadOrCreateRegistry(pageId)
        registry.IsRoot = true
        registry.Enabled = root.enabled
        registry.Folder = root.folder
        saveRegistry(registry)

    if len(cleanedRoots) != len(roots):
        writeRootMd(cleanedRoots)  // Remove duplicates from file
```

### Edge Cases

1. **Root in registry but not in root.md**: Orphaned, will be deleted by `cleanup`
2. **Root in root.md but registry missing**: Create registry on startup
3. **Duplicate entries in root.md**: Keep first occurrence, remove others, rewrite file
4. **Invalid URL in root.md**: Log error, skip entry
5. **Webhook discovers page with unknown root**: Page is queued but not synced until root is added to `root.md`
6. **Cleanup with no orphans**: No-op, logs "No orphaned pages found"

## Files to Modify

- `internal/sync/add.go`: Remove
- `internal/sync/pull.go`: Filter by enabled roots
- `internal/sync/process.go`: Check enabled status before processing
- `internal/sync/state.go`: Add `Enabled` field to `PageRegistry`
- `internal/sync/registry.go`: Add functions to read/write `root.md`
- `internal/sync/rootmd.go`: New file for `root.md` parsing, page ID normalization, and startup reconciliation
- `internal/sync/cleanup.go`: New file for cleanup logic (find and delete orphaned pages)
- `cmd/add.go`: Remove
- `cmd/cleanup.go`: New file for `cleanup` command
- `cmd/*.go`: Add startup reconciliation call before command execution

## Example Workflow

```bash
# User edits root.md to add roots:
# | folder  | enabled | url |
# |---------|---------|-----|
# | tech    | [x]     | https://notion.so/Tech-abc123 |
# | archive | [x]     | https://notion.so/Archive-xyz789 |

# Run any command - registries are created/synced automatically
./ntnsync pull

# User edits root.md to disable archive:
# | archive | [ ]     | https://notion.so/Archive-xyz789 |

# Subsequent syncs skip archive and all its children
./ntnsync pull   # Only queries tech pages
./ntnsync sync   # Only processes tech pages

# User removes archive from root.md entirely, then cleans up
./ntnsync cleanup --dry-run  # Preview what will be deleted
./ntnsync cleanup            # Delete orphaned pages and registries

# If user accidentally adds duplicate (same page ID, different URL):
# | tech    | [x]     | https://notion.so/Tech-abc123 |
# | other   | [x]     | https://notion.so/abc123 |        <-- same page ID!
# On next startup, duplicate is removed and file is rewritten
```
