# Maximum File Size Limit

**Date**: 2026-01-24
**Status**: Implemented

## Overview

Add a configurable maximum file size limit for downloaded files (images, attachments). Files exceeding the limit are skipped with a warning.

## Motivation

Large files (high-resolution images, PDFs, attachments) can:
- Bloat the git repository size significantly
- Slow down sync operations
- Cause issues with git hosting services that have file size limits (e.g., GitHub's 100MB limit)
- Consume excessive disk space on the local filesystem

A configurable limit allows users to control which files are synced based on their size.

## Design

### Environment Variable

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_MAX_FILE_SIZE` | Maximum file size to download | `5MB` |

#### `NTN_MAX_FILE_SIZE`

**Valid values**:
- Size with unit suffix: `1MB`, `5MB`, `10MB`, `100KB`, `1GB`
- Bytes as plain number: `5242880` (5MB in bytes)
- `0` or unset - Uses default of 5MB

**Supported units** (case-insensitive):
- `B` - bytes
- `KB` - kilobytes (1024 bytes)
- `MB` - megabytes (1024 KB)
- `GB` - gigabytes (1024 MB)

### Behavior

When a file exceeds the maximum size:
1. Skip the download
2. Log a warning with file details (name, size, limit)
3. Add a placeholder comment in the markdown indicating the file was skipped
4. Continue processing the rest of the page

### Affected File Types

The limit applies to all downloaded binary files:
- Images (embedded in pages)
- File attachments
- PDFs
- Any other binary content from Notion

### Placeholder Format

When a file is skipped, insert a placeholder in the markdown:

```markdown
<!-- File skipped: image.png (15.2 MB exceeds 5 MB limit) -->
```

For images that would normally render:
```markdown
<!-- Image skipped: screenshot.png (15.2 MB exceeds 5 MB limit) -->
![screenshot.png](skipped: file too large)
```

## Implementation

### Parse Size Value

```go
func parseMaxFileSize() int64 {
    val := os.Getenv("NTN_MAX_FILE_SIZE")
    if val == "" || val == "0" {
        return 5 * 1024 * 1024 // 5MB default
    }

    // Try parsing as plain bytes
    if bytes, err := strconv.ParseInt(val, 10, 64); err == nil {
        return bytes
    }

    // Parse with unit suffix
    val = strings.ToUpper(strings.TrimSpace(val))

    units := map[string]int64{
        "B":  1,
        "KB": 1024,
        "MB": 1024 * 1024,
        "GB": 1024 * 1024 * 1024,
    }

    for suffix, multiplier := range units {
        if strings.HasSuffix(val, suffix) {
            numStr := strings.TrimSuffix(val, suffix)
            if num, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64); err == nil {
                return int64(num * float64(multiplier))
            }
        }
    }

    // Invalid format, use default
    slog.Warn("Invalid NTN_MAX_FILE_SIZE format, using default",
        "value", val,
        "default", "5MB",
    )
    return 5 * 1024 * 1024
}
```

### File Size Check

```go
func (c *Crawler) downloadFile(ctx context.Context, url, destPath string) error {
    maxSize := parseMaxFileSize()

    // First, do a HEAD request to check size
    resp, err := http.Head(url)
    if err != nil {
        return fmt.Errorf("HEAD request failed: %w", err)
    }
    resp.Body.Close()

    contentLength := resp.ContentLength
    if contentLength > maxSize {
        slog.WarnContext(ctx, "File exceeds size limit, skipping",
            "url", url,
            "size", formatBytes(contentLength),
            "limit", formatBytes(maxSize),
        )
        return ErrFileTooLarge
    }

    // Proceed with download
    return c.doDownload(ctx, url, destPath, maxSize)
}

func (c *Crawler) doDownload(ctx context.Context, url, destPath string, maxSize int64) error {
    resp, err := http.Get(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Use LimitReader as a safety net even if Content-Length was within limits
    // (server might send more data than advertised)
    limitedReader := io.LimitReader(resp.Body, maxSize+1)

    file, err := os.Create(destPath)
    if err != nil {
        return err
    }
    defer file.Close()

    written, err := io.Copy(file, limitedReader)
    if err != nil {
        return err
    }

    if written > maxSize {
        os.Remove(destPath)
        return ErrFileTooLarge
    }

    return nil
}
```

### Error Type

```go
var ErrFileTooLarge = errors.New("file exceeds maximum size limit")
```

### Format Helper

```go
func formatBytes(bytes int64) string {
    const unit = 1024
    if bytes < unit {
        return fmt.Sprintf("%d B", bytes)
    }
    div, exp := int64(unit), 0
    for n := bytes / unit; n >= unit; n /= unit {
        div *= unit
        exp++
    }
    return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
```

## Examples

### Default behavior (5MB limit)

```bash
./ntnsync sync

# Output:
# WARN File exceeds size limit, skipping url=https://... size=15.2 MB limit=5 MB
# INFO Synced page page_id=abc123
```

### Custom limit

```bash
# Allow larger files (50MB)
NTN_MAX_FILE_SIZE=50MB ./ntnsync sync

# Stricter limit (1MB)
NTN_MAX_FILE_SIZE=1MB ./ntnsync sync

# No limit (1GB is effectively unlimited for most use cases)
NTN_MAX_FILE_SIZE=1GB ./ntnsync sync
```

### In CI/CD

```yaml
jobs:
  sync:
    runs-on: ubuntu-latest
    env:
      NTN_MAX_FILE_SIZE: 10MB
    steps:
      - run: ./ntnsync sync
```

## Success Criteria

- [x] `NTN_MAX_FILE_SIZE` environment variable is parsed correctly
- [x] Default limit is 5MB when variable is unset
- [x] Files exceeding limit are skipped with a warning
- [x] Size units (KB, MB, GB) are parsed correctly (case-insensitive)
- [x] Plain byte values are accepted
- [x] Invalid values fall back to default (silently, no warning needed)
- [ ] Placeholder comments are inserted in markdown for skipped files (not implemented - returns original URL instead)
- [x] HEAD request is used to check size before downloading (when supported)
- [x] LimitReader is used as a safety net during download
- [ ] Documentation updated (CLAUDE.md, docs/cli-commands.md)
