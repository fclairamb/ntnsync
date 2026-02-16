# Permanent Error Detection in Queue Processing

**Date:** 2026-02-17
**Status:** Proposed

## Problem

Queue entries that fail with **permanent Notion API errors** are retried indefinitely, accumulating over time. After weeks of operation, 49 queue files remain stuck in `/tmp/ntnsync/.notion-sync/queue/`, with 36 of them failing on every sync cycle due to errors that will never resolve.

### Observed errors in production

From 100k+ log lines analyzed:

| Count | Notion API Code | HTTP Status | Example Message |
|------:|----------------|-------------|-----------------|
| 32 | `object_not_found` | 404 | "Could not find block with ID: ... Make sure the relevant pages and databases are shared with your integration." |
| 2 | `validation_error` | 400 | Database has no queryable data sources |
| 1 | `object_not_found` | 404 | Database not accessible to integration |
| 1 | `validation_error` | 400 | "... is a block, not a page" |

**36 entries updated (kept for retry) vs 13 deleted (consumed)** — a 73% failure rate, all permanent.

### Root cause

In `internal/sync/process.go`, both `processNewFormatEntry` and `processLegacyFormatEntry` treat all `processPage()` errors identically — the failed page is added to `remaining` and the queue entry is kept:

```go
filesCount, err := c.processPage(ctx, pageID, ...)
if err != nil {
    c.logger.ErrorContext(ctx, "failed to process page", "page_id", pageID, "error", err)
    remaining = append(remaining, *queuePage)  // retried forever
    continue
}
```

There is no distinction between transient errors (rate limits, network issues) and permanent errors (page deleted, not shared with integration, wrong object type).

## Solution

### 1. Add error classification to the Notion API error types

**File:** `internal/notion/types.go`

Add methods to `APIError` to classify errors:

```go
// IsPermanent returns true if this error will never resolve by retrying.
// These are errors where the resource doesn't exist, isn't shared with the
// integration, or is the wrong type.
func (e *APIError) IsPermanent() bool {
    switch e.Status {
    case 401: // Unauthorized - token is invalid
        return true
    case 403: // Forbidden - integration doesn't have access
        return true
    case 404: // Not Found - resource doesn't exist or not shared
        return true
    }

    // Some 400 errors are permanent (wrong object type, no data sources)
    if e.Status == 400 {
        switch e.Code {
        case "validation_error":
            return true
        }
    }

    return false
}
```

### 2. Add a helper to check if a wrapped error is permanent

**File:** `internal/notion/types.go`

Since errors from `processPage()` are wrapped with `fmt.Errorf("fetch page: %w", err)`, we need an unwrapping helper:

```go
// IsPermanentError checks if an error (possibly wrapped) is a permanent Notion API error.
func IsPermanentError(err error) bool {
    var apiErr *APIError
    if errors.As(err, &apiErr) {
        return apiErr.IsPermanent()
    }
    return false
}
```

### 3. Drop queue entries on permanent errors

**File:** `internal/sync/process.go`

In both `processNewFormatEntry` (~line 293) and `processLegacyFormatEntry` (~line 339), when `processPage()` returns an error, check if it's permanent before adding to `remaining`:

```go
filesCount, err := c.processPage(ctx, pageID, entry.Folder, entry.Type == queueTypeInit, entry.ParentID)
if err != nil {
    if notion.IsPermanentError(err) {
        c.logger.WarnContext(ctx, "dropping page from queue (permanent error)",
            "page_id", pageID, "error", err)
        stats.totalSkipped++
        continue // do NOT add to remaining
    }
    c.logger.ErrorContext(ctx, "failed to process page (will retry)",
        "page_id", pageID, "error", err)
    remaining = append(remaining, *queuePage)
    continue
}
```

This change applies to both `processNewFormatEntry` and `processLegacyFormatEntry`.

### 4. Log summary of dropped pages

**File:** `internal/sync/process.go`

Add a `totalDropped` counter to `queueProcessingStats`:

```go
type queueProcessingStats struct {
    totalProcessed    int
    totalSkipped      int
    totalDropped      int  // pages dropped due to permanent errors
    totalFilesWritten int
}
```

Include it in the completion log (~line 201):

```go
logAttrs := []any{
    "processed", totalProcessed,
    "skipped", totalSkipped,
    "dropped", totalDropped,
    "files_written", totalFilesWritten,
    ...
}
```

## Error Classification Reference

| HTTP Status | Notion Code | Permanent? | Reason |
|-------------|------------|:----------:|--------|
| 400 | `validation_error` | Yes | Wrong object type, missing data sources, invalid request |
| 401 | `unauthorized` | Yes | Invalid API token |
| 403 | `restricted_resource` | Yes | Integration lacks access |
| 404 | `object_not_found` | Yes | Page/block deleted or not shared |
| 409 | `conflict_error` | No | Concurrent edit, retry may succeed |
| 429 | `rate_limited` | No | Already handled by client retry logic |
| 500 | `internal_server_error` | No | Notion server issue, transient |
| 502 | — | No | Gateway error, transient |
| 503 | `service_unavailable` | No | Notion down, transient |

Note: 429 rate limits are already handled with exponential backoff in `internal/notion/client.go` (`handleRateLimit`), so they won't reach the queue processing layer.

## Scope

### Files changed

- `internal/notion/types.go` — add `IsPermanent()` method and `IsPermanentError()` helper
- `internal/sync/process.go` — classify errors in `processNewFormatEntry` and `processLegacyFormatEntry`, add `totalDropped` counter

### Files not changed

- `internal/notion/client.go` — rate limit retry logic is already correct
- `internal/queue/queue.go` — no changes to queue storage format

## Testing

- Unit test `IsPermanent()` on `APIError` with each status/code combination
- Unit test `IsPermanentError()` with wrapped errors (`fmt.Errorf("fetch page: %w", apiErr)`)
- Integration test: verify a queue entry with a non-existent page ID is deleted after one attempt, not kept for retry

## Trade-offs

1. **False positive risk**: A 403/404 error could theoretically become resolvable if someone shares the page with the integration later. However, the page would be re-queued by the next `pull` operation when Notion reports it as changed, so no data is permanently lost.

2. **No dead-letter queue**: Dropped pages are only visible in logs. If needed later, a dead-letter directory could be added, but the log-based approach keeps the implementation simple and the queue clean.
