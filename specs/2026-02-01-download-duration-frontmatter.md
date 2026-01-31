# Download Duration in Frontmatter

**Date:** 2026-02-01
**Status:** Draft

## Problem

Currently, the frontmatter tracks when a page was synced (`last_synced`) but not how long the download took. This information would be valuable for:

1. **Performance monitoring**: Identify pages that take unusually long to sync
2. **Debugging**: Detect slow API responses or network issues
3. **Optimization**: Understand which pages have heavy content (many blocks, files)
4. **Transparency**: Users can see the sync cost of each page

## Proposed Solution

Add a `download_duration` field to the frontmatter that records the total time taken to fully download a page, including:

- Fetching page metadata
- Fetching all blocks (with pagination)
- Fetching child blocks recursively
- Downloading embedded files (images, PDFs, etc.)

### Frontmatter Example

```yaml
---
ntnsync_version: 0.5.0
notion_id: 2c536f5e-48f4-4234-ad8d-73a1a148e95d
title: "Architecture Overview"
notion_type: page
notion_folder: tech
file_path: tech/wiki/architecture.md
last_edited: 2026-01-15T10:30:00Z
last_synced: 2026-02-01T14:25:00Z
download_duration: 1.234s
notion_url: https://notion.so/Architecture-2c536f5e48f44234ad8d73a1a148e95d
is_root: false
---
```

### Format

- Use Go's `time.Duration.String()` format: `1.234s`, `500ms`, `2m30s`
- Human-readable and precise
- Consistent with standard Go duration formatting

## Implementation

### Files to Modify

1. **`internal/converter/converter.go`**:
   - Add `DownloadDuration time.Duration` to `ConvertOptions`
   - Output `download_duration` in `generateFrontmatter()`

2. **`internal/sync/crawler.go`**:
   - Track start time before fetching page
   - Calculate duration after all downloads complete
   - Pass duration to converter options

### Code Changes

#### ConvertOptions (converter.go)

```go
type ConvertOptions struct {
    Folder           string
    PageTitle        string
    FilePath         string
    LastSynced       time.Time
    NotionType       string
    IsRoot           bool
    ParentID         string
    FileProcessor    FileProcessor
    SimplifiedDepth  int
    DownloadDuration time.Duration // NEW: Time to download page completely
}
```

#### generateFrontmatter (converter.go)

Add after `last_synced`:

```go
// Download duration
if opts.DownloadDuration > 0 {
    builder.WriteString(fmt.Sprintf("download_duration: %s\n", opts.DownloadDuration))
}
```

#### crawler.go

In the page processing function, wrap the download logic:

```go
startTime := time.Now()

// ... existing code to fetch page, blocks, files ...

downloadDuration := time.Since(startTime)

// Pass to converter
opts := &converter.ConvertOptions{
    // ... existing fields ...
    DownloadDuration: downloadDuration,
}
```

## Measurement Scope

The duration should include:

| Operation | Included |
|-----------|----------|
| Page metadata fetch | Yes |
| Block fetching (all pages) | Yes |
| Child block recursion | Yes |
| File downloads (images, PDFs) | Yes |
| Markdown conversion | No |
| File writing | No |
| Registry updates | No |

The goal is to measure Notion API + file download time, not local processing.

## Edge Cases

1. **Cached files**: If files are already downloaded (file registry exists), they're skipped. Duration reflects actual network activity.

2. **Depth-limited pages**: Pages with `simplified_depth` will have shorter download times since child blocks aren't fetched.

3. **Database pages**: Same treatment as regular pages.

4. **Failed downloads**: If download fails, no frontmatter is written, so no duration recorded.

## Testing

### Unit Tests

1. Verify `download_duration` appears in frontmatter when set
2. Verify `download_duration` is omitted when zero
3. Verify duration format is correct (`1.234s`)

### Integration Tests

1. Sync a page and verify duration is present and reasonable
2. Sync a page with files, verify duration includes file download time
3. Re-sync a page with cached files, verify duration is shorter

## Documentation Updates

Update `docs/file-architecture.md` to document the new frontmatter field:

```markdown
| `download_duration` | duration | Time to download page from Notion (e.g., `1.234s`) |
```

## Future Considerations

- Could add `download_duration_blocks` and `download_duration_files` for detailed breakdown
- Could track this in the registry for historical analysis
- Could add a `--slow` flag to `list` command to show pages with high download times
