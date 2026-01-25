# JSON Logging Format Support

**Date**: 2026-01-24
**Status**: Draft

## Overview

Add support for JSON-formatted logging via the `NTN_LOG_FORMAT` environment variable. This enables structured logging for easier parsing, monitoring, and integration with log aggregation systems.

## Motivation

Current approach:
- Logs are output in a human-readable text format only
- Difficult to parse programmatically in CI/CD pipelines
- Hard to integrate with log aggregation systems (CloudWatch, Datadog, ELK stack, etc.)
- No structured fields for filtering or querying

With `NTN_LOG_FORMAT=json`:
- Machine-readable structured logs with consistent field names
- Easy integration with log aggregation and monitoring systems
- Better support for automated alerting and analysis
- Queryable log fields (level, timestamp, message, component, etc.)
- Compatible with 12-factor app principles for containerized deployments

## Design

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_LOG_FORMAT` | Log output format | `text` |

#### `NTN_LOG_FORMAT`

**Valid values**:
- `text` or unset - Human-readable text format (default)
- `json` - JSON-formatted structured logs

Invalid values should fall back to `text` format with a warning logged.

### Log Formats

#### Text Format (Default)

Current human-readable format:
```
2026-01-24T10:30:45Z INFO  Starting sync
2026-01-24T10:30:46Z DEBUG Processing page: "Product Requirements"
2026-01-24T10:30:47Z WARN  Rate limit approaching: 2 requests remaining
2026-01-24T10:30:48Z ERROR Failed to download image: connection timeout
```

#### JSON Format

Structured logs with consistent fields:
```json
{"timestamp":"2026-01-24T10:30:45Z","level":"info","message":"Starting sync"}
{"timestamp":"2026-01-24T10:30:46Z","level":"debug","message":"Processing page","page_title":"Product Requirements","page_id":"abc123"}
{"timestamp":"2026-01-24T10:30:47Z","level":"warn","message":"Rate limit approaching","remaining_requests":2}
{"timestamp":"2026-01-24T10:30:48Z","level":"error","message":"Failed to download image","error":"connection timeout","url":"https://..."}
```

### Standard JSON Fields

All JSON log entries include:
- `timestamp` (string, RFC3339) - Log event time
- `level` (string) - Log level: `debug`, `info`, `warn`, `error`
- `message` (string) - Primary log message

### Contextual Fields

Additional fields based on context:
- `page_id` (string) - Notion page ID being processed
- `page_title` (string) - Page title
- `file_path` (string) - Local file path
- `queue_type` (string) - Queue type: `init` or `update`
- `error` (string) - Error message for error-level logs
- `duration_ms` (number) - Operation duration in milliseconds
- `bytes` (number) - File or data size
- `url` (string) - External URL being accessed
- `database_id` (string) - Notion database ID
- `remaining_requests` (number) - API rate limit remaining

### Implementation

#### Using Go's `slog` Package

Go 1.21+ provides the `log/slog` package for structured logging:

```go
package logging

import (
    "log/slog"
    "os"
    "strings"
)

// Format represents the log format
type Format string

const (
    FormatText Format = "text"
    FormatJSON Format = "json"
)

// GetFormat returns the configured log format from environment
func GetFormat() Format {
    val := strings.ToLower(os.Getenv("NTN_LOG_FORMAT"))
    switch val {
    case "json":
        return FormatJSON
    case "text", "":
        return FormatText
    default:
        // Invalid format - warn and fall back to text
        slog.Warn("Invalid NTN_LOG_FORMAT value, using text format", "value", val)
        return FormatText
    }
}

// NewLogger creates a logger with the appropriate format
func NewLogger(verbose bool) *slog.Logger {
    format := GetFormat()
    level := slog.LevelInfo
    if verbose {
        level = slog.LevelDebug
    }

    var handler slog.Handler
    opts := &slog.HandlerOptions{Level: level}

    switch format {
    case FormatJSON:
        handler = slog.NewJSONHandler(os.Stdout, opts)
    default:
        handler = slog.NewTextHandler(os.Stdout, opts)
    }

    return slog.New(handler)
}
```

#### Usage in Application Code

```go
// Initialize logger at startup
logger := logging.NewLogger(verbose)
slog.SetDefault(logger)

// Simple logging
slog.Info("Starting sync")
slog.Debug("Processing page", "page_id", pageID, "page_title", title)

// Logging with error
slog.Error("Failed to download image",
    "error", err.Error(),
    "url", imageURL,
    "page_id", pageID)

// Logging with multiple fields
slog.Info("Page synced successfully",
    "page_id", pageID,
    "page_title", title,
    "file_path", filePath,
    "duration_ms", elapsed.Milliseconds(),
    "bytes", fileSize)
```

#### Logging in Different Components

```go
// Queue processing
slog.Info("Processing queue item",
    "queue_type", queueType,
    "page_id", pageID,
    "page_title", title)

// Database operations
slog.Debug("Querying database",
    "database_id", dbID,
    "database_title", dbTitle,
    "filter_count", len(filters))

// Git operations
slog.Info("Git commit created",
    "files_changed", changedFiles,
    "commit_message", msg)

// API rate limiting
slog.Warn("Rate limit approaching",
    "remaining_requests", remaining,
    "reset_time", resetTime)

// Error scenarios
slog.Error("Page sync failed",
    "page_id", pageID,
    "error", err.Error(),
    "retry_count", retries)
```

### Migration from Current Logging

Replace existing log calls:
```go
// Before
fmt.Printf("Processing page: %s\n", title)
log.Printf("ERROR: Failed to sync: %v", err)

// After (text format output)
slog.Info("Processing page", "page_title", title)
// 2026-01-24T10:30:45Z INFO Processing page page_title="Product Requirements"

// After (JSON format output)
slog.Info("Processing page", "page_title", title)
// {"timestamp":"2026-01-24T10:30:45Z","level":"info","message":"Processing page","page_title":"Product Requirements"}

slog.Error("Failed to sync", "error", err.Error())
// {"timestamp":"2026-01-24T10:30:46Z","level":"error","message":"Failed to sync","error":"connection timeout"}
```

## Examples

### Example 1: Local development (default text format)

```bash
./ntnsync sync -v

# Output:
# 2026-01-24T10:30:45Z INFO  Starting sync
# 2026-01-24T10:30:46Z DEBUG Processing page page_id=abc123 page_title="Product Requirements"
# 2026-01-24T10:30:47Z INFO  Page synced successfully page_id=abc123 file_path=tech/product-requirements.md
```

### Example 2: CI/CD with JSON logs

```bash
export NTN_LOG_FORMAT=json
./ntnsync sync -v

# Output (each line is a separate JSON object):
# {"timestamp":"2026-01-24T10:30:45Z","level":"info","message":"Starting sync"}
# {"timestamp":"2026-01-24T10:30:46Z","level":"debug","message":"Processing page","page_id":"abc123","page_title":"Product Requirements"}
# {"timestamp":"2026-01-24T10:30:47Z","level":"info","message":"Page synced successfully","page_id":"abc123","file_path":"tech/product-requirements.md","duration_ms":1250,"bytes":4096}
```

### Example 3: Docker container with JSON logs

```dockerfile
FROM golang:1.22-alpine

WORKDIR /app
COPY ntnsync .

ENV NTN_LOG_FORMAT=json
ENV NTN_COMMIT=true

CMD ["./ntnsync", "sync"]
```

### Example 4: GitHub Actions with JSON parsing

```yaml
name: Notion Sync
on:
  schedule:
    - cron: '0 * * * *'

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Sync Notion pages
        env:
          NOTION_TOKEN: ${{ secrets.NOTION_TOKEN }}
          NTN_LOG_FORMAT: json
          NTN_COMMIT: true
        run: |
          ./ntnsync pull --since 2h
          ./ntnsync sync | tee sync.log

      - name: Check for errors
        run: |
          # Parse JSON logs and fail if any errors
          if jq -e 'select(.level == "error")' sync.log > /dev/null; then
            echo "Errors found in sync:"
            jq 'select(.level == "error")' sync.log
            exit 1
          fi

      - name: Report sync stats
        run: |
          # Extract sync statistics from JSON logs
          echo "Pages synced:"
          jq -r 'select(.message == "Page synced successfully") | .page_title' sync.log
```

### Example 5: CloudWatch Logs integration

```bash
# In AWS ECS/Fargate task definition
{
  "containerDefinitions": [{
    "name": "ntnsync",
    "image": "ntnsync:latest",
    "environment": [
      {"name": "NTN_LOG_FORMAT", "value": "json"},
      {"name": "NTN_COMMIT", "value": "true"}
    ],
    "logConfiguration": {
      "logDriver": "awslogs",
      "options": {
        "awslogs-group": "/ecs/ntnsync",
        "awslogs-region": "us-east-1",
        "awslogs-stream-prefix": "sync"
      }
    }
  }]
}

# CloudWatch Insights query
fields @timestamp, level, message, page_title, error
| filter level = "error"
| sort @timestamp desc
| limit 20
```

### Example 6: Invalid format fallback

```bash
export NTN_LOG_FORMAT=invalid
./ntnsync sync

# Output (falls back to text format):
# 2026-01-24T10:30:45Z WARN Invalid NTN_LOG_FORMAT value, using text format value=invalid
# 2026-01-24T10:30:45Z INFO Starting sync
```

## Benefits

### For Development
- Structured context makes debugging easier
- Consistent field names across all logs
- Easy to grep/filter by specific fields

### For Operations
- Direct integration with log aggregation systems
- Machine-readable for automated monitoring
- Query logs by specific fields (page_id, error type, etc.)
- Generate metrics from log data

### For CI/CD
- Parse logs programmatically in pipelines
- Fail builds based on specific error conditions
- Extract statistics from sync operations
- Generate reports from structured data

## Success Criteria

- [ ] `NTN_LOG_FORMAT` environment variable supported
- [ ] `json` value produces valid JSON log output
- [ ] `text` value produces human-readable text output
- [ ] Invalid values fall back to text format with warning
- [ ] All log levels supported: debug, info, warn, error
- [ ] Standard fields included in all JSON logs: timestamp, level, message
- [ ] Contextual fields added appropriately (page_id, error, etc.)
- [ ] Existing logging replaced with slog throughout codebase
- [ ] Documentation updated (CLAUDE.md, docs/development.md)
- [ ] JSON format works correctly with `-v` verbose flag
- [ ] No breaking changes to default text output format
