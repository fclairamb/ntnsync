# Streaming File Downloads

## Problem

Previously, when downloading files from Notion, the entire file content was loaded into memory before being written to disk. This happened in `internal/sync/file.go:169-170`:

```go
limitedReader := io.LimitReader(resp.Body, maxSizeBytes+1)
data, err := io.ReadAll(limitedReader)
```

While the `NTN_MAX_FILE_SIZE` limit (default 5MB) provided some protection, this approach:
- Consumed memory proportional to file size for each download
- Could cause memory pressure when processing many files concurrently
- Limited the practical maximum file size that can be downloaded

## Solution

Stream HTTP response bodies directly to disk using `io.Copy()` instead of loading them into memory.

## Implementation

### Architecture Change: Transaction-Only Writes

All write operations (`Write`, `WriteStream`, `Delete`, `Mkdir`) are now exclusively available through the `Transaction` interface, not directly on `Store`. This provides:
- Cleaner separation of read and write operations
- Explicit transaction boundaries for all modifications
- Consistent patterns for git commit handling

### Store Interface (Read-only + Transaction Management)

```go
type Store interface {
    // Read operations
    Read(ctx context.Context, path string) ([]byte, error)
    Exists(ctx context.Context, path string) (bool, error)
    List(ctx context.Context, dir string) ([]FileInfo, error)

    // Transaction management - all writes go through transactions
    BeginTx(ctx context.Context) (Transaction, error)

    // Remote operations
    Push(ctx context.Context) error

    // Concurrency control
    Lock()
    Unlock()
}
```

### Transaction Interface (All Write Operations)

```go
type Transaction interface {
    // Write operations - applied immediately to filesystem
    Write(path string, content []byte) error
    WriteStream(path string, reader io.Reader) (int64, error)
    Delete(path string) error
    Mkdir(path string) error

    // Git operations
    Commit(message string) error
    Rollback() error
}
```

### WriteStream Implementation

The `WriteStream` method in `localTransaction`:
- Uses atomic temp file + rename pattern for safety
- Streams data via `io.Copy()` with constant memory usage (~32KB buffer)
- Tracks modified paths for git staging on commit

### Crawler Transaction Management

The `Crawler` struct now holds a `tx` field and provides:
- `EnsureTransaction(ctx)` - creates transaction if needed
- `SetTransaction(tx)` - sets external transaction
- `Commit(ctx, message)` - commits current transaction

## Benefits

- **Constant memory usage**: File downloads use only buffer-sized memory (~32KB) regardless of file size
- **Larger file support**: Can safely increase `NTN_MAX_FILE_SIZE` without memory concerns
- **Atomic writes**: Temp file + rename pattern prevents partial files on failure
- **Better concurrency**: Multiple files can download simultaneously without memory multiplication
- **Cleaner architecture**: All writes go through transactions, providing explicit commit boundaries

## Testing

1. All existing tests updated to use transactions
2. New `TestLocalTransaction_WriteStream` tests verify:
   - Basic streaming write
   - Parent directory creation
   - Correct file permissions (0600)
   - Atomic behavior (no temp files left behind)

## Migration

Components that were updated:
- `Crawler` - now manages a transaction via `c.tx`
- `Queue.Manager` - accepts transaction via `SetTransaction(tx)`
- `Webhook.Handler` - creates transaction for event processing
- All sync operations - use `c.tx.Write()` instead of `c.store.Write()`
