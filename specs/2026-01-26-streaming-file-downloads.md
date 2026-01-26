# Streaming File Downloads

## Problem

Currently, when downloading files from Notion, the entire file content is loaded into memory before being written to disk. This happens in `internal/sync/file.go:169-170`:

```go
limitedReader := io.LimitReader(resp.Body, maxSizeBytes+1)
data, err := io.ReadAll(limitedReader)
```

While the `NTN_MAX_FILE_SIZE` limit (default 5MB) provides some protection, this approach:
- Consumes memory proportional to file size for each download
- Could cause memory pressure when processing many files concurrently
- Limits the practical maximum file size that can be downloaded

## Solution

Stream HTTP response bodies directly to disk using `io.Copy()` instead of loading them into memory.

## Implementation

### 1. Update Store Interface

Add a streaming write method to `internal/store/store.go`:

```go
type Store interface {
    // Existing methods...
    Write(ctx context.Context, path string, content []byte) error

    // New streaming method
    WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error)
}
```

### 2. Implement WriteStream in LocalStore

In `internal/store/local.go`:

```go
func (s *LocalStore) WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error) {
    fullPath := s.fullPath(path)

    if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
        return 0, fmt.Errorf("creating directories: %w", err)
    }

    // Write to temp file first, then rename for atomicity
    tmpFile, err := os.CreateTemp(filepath.Dir(fullPath), ".tmp-*")
    if err != nil {
        return 0, fmt.Errorf("creating temp file: %w", err)
    }
    tmpPath := tmpFile.Name()

    // Ensure cleanup on failure
    defer func() {
        tmpFile.Close()
        os.Remove(tmpPath) // No-op if already renamed
    }()

    written, err := io.Copy(tmpFile, reader)
    if err != nil {
        return written, fmt.Errorf("writing content: %w", err)
    }

    if err := tmpFile.Close(); err != nil {
        return written, fmt.Errorf("closing temp file: %w", err)
    }

    if err := os.Chmod(tmpPath, filePerm); err != nil {
        return written, fmt.Errorf("setting permissions: %w", err)
    }

    if err := os.Rename(tmpPath, fullPath); err != nil {
        return written, fmt.Errorf("renaming temp file: %w", err)
    }

    return written, nil
}
```

### 3. Update downloadFile Function

In `internal/sync/file.go`, replace the memory-loading approach:

```go
func (c *Converter) downloadFile(ctx context.Context, url, localPath string) error {
    // ... existing HEAD request logic for size check ...

    // GET request
    resp, err := c.httpClient.Get(url)
    if err != nil {
        return fmt.Errorf("getting file: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("unexpected status: %d", resp.StatusCode)
    }

    // Use LimitReader to enforce max size during streaming
    limitedReader := io.LimitReader(resp.Body, maxSizeBytes+1)

    // Stream directly to file
    written, err := c.store.WriteStream(ctx, localPath, limitedReader)
    if err != nil {
        return fmt.Errorf("writing file: %w", err)
    }

    // Check if we hit the size limit
    if written > maxSizeBytes {
        // Clean up the oversized file
        _ = c.store.Remove(ctx, localPath)
        return fmt.Errorf("file exceeds max size (%d bytes)", maxSizeBytes)
    }

    return nil
}
```

### 4. Add Remove Method to Store

Add a `Remove` method to clean up files that exceed size limits:

```go
// In store.go interface
Remove(ctx context.Context, path string) error

// In local.go implementation
func (s *LocalStore) Remove(ctx context.Context, path string) error {
    return os.Remove(s.fullPath(path))
}
```

## Benefits

- **Constant memory usage**: File downloads use only buffer-sized memory (~32KB by default for `io.Copy`)
- **Larger file support**: Can safely increase `NTN_MAX_FILE_SIZE` without memory concerns
- **Atomic writes**: Temp file + rename pattern prevents partial files on failure
- **Better concurrency**: Multiple files can download simultaneously without memory multiplication

## Testing

1. Download a file larger than available memory (with increased `NTN_MAX_FILE_SIZE`)
2. Verify file integrity after streaming download
3. Test interruption scenarios (network failure mid-download)
4. Confirm oversized files are cleaned up properly

## Migration

The existing `Write()` method remains for small content like markdown files and metadata JSON. The new `WriteStream()` is specifically for file downloads from Notion.
