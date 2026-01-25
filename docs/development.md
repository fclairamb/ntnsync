# Development Guidelines

## Logging

### Log Format Configuration

Set `NTN_LOG_FORMAT` environment variable to control output:
- `text` (default): Human-readable format for development
- `json`: Structured JSON for CI/CD and log aggregation

```bash
# JSON format for production/CI
NTN_LOG_FORMAT=json ./ntnsync sync

# Text format (default)
./ntnsync sync -v
```

### Using slog

Always use the logger with context when available:

```go
// Good
c.logger.DebugContext(ctx, "processing page", "page_id", pageID)
c.logger.InfoContext(ctx, "sync complete", "pages", count)
slog.DebugContext(ctx, "operation started")

// Avoid (when context is available)
c.logger.Debug("processing page", "page_id", pageID)
slog.Debug("operation started")
```

Using context-aware logging enables better tracing and correlation of log entries.

## Code Organization

**Main packages**:
- `internal/cmd/` - CLI command handlers
- `internal/notion/` - Notion API client and types
- `internal/sync/` - Sync logic (crawler, converter, queue, state)
- `internal/store/` - Storage abstraction (git-backed filesystem)

## Testing

```bash
go test ./...
```

## Building

```bash
go build -o ntnsync .
```
