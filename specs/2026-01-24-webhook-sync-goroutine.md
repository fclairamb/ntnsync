# Webhook Sync Goroutine

**Date:** 2026-01-24
**Status:** Proposal

## Overview

### Problem

The current webhook implementation queues page changes but requires a separate `sync` command to process them. For real-time syncing, the webhook server should automatically process queued items.

### Goal

The `serve` command should run a background sync goroutine that:
1. Processes queued items automatically
2. Waits efficiently when the queue is empty
3. Wakes up when new webhook events arrive
4. Safely handles concurrent access to the repository

## Design

### Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│                         Webhook Server                              │
│                                                                     │
│  ┌──────────────┐         ┌─────────────────────────────────────┐  │
│  │   HTTP       │         │         Sync Goroutine              │  │
│  │   Handler    │         │                                     │  │
│  │              │  notify │  ┌────────┐  ┌─────────────────┐    │  │
│  │  webhook ────┼────────►│  │ Wait   │─►│ Fetch from API  │    │  │
│  │  received    │         │  │ Signal │  │ (NO LOCK)       │    │  │
│  │              │         │  └────────┘  └───────┬─────────┘    │  │
│  └──────────────┘         │                      │              │  │
│                           │                      ▼              │  │
│                           │              ┌──────────────┐       │  │
│                           │              │ Convert to   │       │  │
│                           │              │ Markdown     │       │  │
│                           │              │ (NO LOCK)    │       │  │
│                           │              └──────┬───────┘       │  │
│                           │                     │               │  │
│                           │                     ▼               │  │
│                           │              ┌──────────────┐       │  │
│                           │              │ Write Files  │       │  │
│                           │              │ (WITH LOCK)  │       │  │
│                           │              └──────┬───────┘       │  │
│                           └─────────────────────┼───────────────┘  │
│                                                 ▼                  │
│  ┌─────────────┐                       ┌──────────────┐            │
│  │ Notion API  │                       │  Repository  │            │
│  │ (external)  │                       │  (Store)     │            │
│  └─────────────┘                       └──────────────┘            │
└────────────────────────────────────────────────────────────────────┘
```

### Components

#### 1. Store Mutex

The `Store` interface gains locking methods to coordinate access:

```go
type Store interface {
    // ... existing methods ...

    // Lock acquires exclusive access to the store
    Lock()
    // Unlock releases exclusive access
    Unlock()
}
```

This belongs on the Store because:
- The Store is the shared resource being protected
- Multiple components may need coordinated access (CLI, sync worker, etc.)
- Keeps locking close to the resource it protects

#### 2. SyncWorker

A new component that manages the background sync process.

```go
type SyncWorker struct {
    crawler   *sync.Crawler
    store     store.Store
    notify    chan struct{}   // Signal channel for new work
    logger    *slog.Logger
}

func NewSyncWorker(crawler *sync.Crawler, store store.Store, logger *slog.Logger) *SyncWorker

func (w *SyncWorker) Start(ctx context.Context) error  // Blocking, runs until context cancelled
func (w *SyncWorker) Notify()                          // Signal that new work is available
```

#### 3. Sync Loop

The sync goroutine follows this pattern:

```go
func (w *SyncWorker) Start(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-w.notify:
            // Drain any additional signals
            w.drainNotify()
        }

        // Process all queued items (lock is acquired per-file, not for entire queue)
        err := w.processQueue(ctx)
        if err != nil {
            w.logger.Error("sync failed", "error", err)
            // Continue processing, don't exit on errors
        }
    }
}

func (w *SyncWorker) drainNotify() {
    for {
        select {
        case <-w.notify:
            // Discard additional signals
        default:
            return
        }
    }
}
```

#### 4. Webhook Handler Integration

The webhook handler has two responsibilities:
1. **Store** the event in the queue
2. **Notify** the sync goroutine

The handler does NOT perform any sync operations itself - it returns immediately after queuing.

```go
func (h *WebhookHandler) handlePageChange(ctx context.Context, event WebhookEvent) {
    pageID := event.GetEntityID()

    // 1. Store in queue (fast, no API calls)
    filename, err := h.queueManager.CreateWebhookEntry(ctx, pageID, folder)
    if err != nil {
        h.logger.Error("failed to queue page", "error", err)
        return
    }

    h.logger.Info("page queued", "page_id", pageID, "queue_file", filename)

    // 2. Notify sync goroutine (non-blocking)
    h.syncWorker.Notify()
}
```

### Webhook Processing Flow

```
Webhook received
       │
       ▼
┌──────────────────┐
│ 1. Queue event   │  ← Fast, just writes a JSON file
│    (page ID)     │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ 2. Notify sync   │  ← Non-blocking channel send
│    goroutine     │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ 3. Return 200 OK │  ← Immediate response to Notion
└──────────────────┘

         ... later, asynchronously ...

┌──────────────────┐
│ Sync goroutine   │
│ wakes up         │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│ Process queue    │  ← Fetch API, convert, write files
└──────────────────┘
```

This separation ensures:
- **Fast webhook responses**: Notion doesn't timeout waiting for sync
- **Reliable delivery**: Event is persisted before acknowledging
- **Decoupled processing**: Sync can fail/retry without affecting webhook handler

### Mutex Strategy

The mutex protects **file operations only**, never API calls. This avoids blocking page updates while waiting for slow network requests.

| Operation | Requires Lock |
|-----------|--------------|
| Notion API calls (fetch page, blocks) | **No** |
| Queue read/write | Yes |
| Markdown file write | Yes |
| Git commit/push | Yes |
| Registry read/write | Yes |

### Sync Flow with Lock Granularity

```go
func (w *SyncWorker) syncPage(ctx context.Context, pageID string) error {
    // 1. Fetch from API - NO LOCK (can be slow, don't block others)
    page, err := w.client.GetPage(ctx, pageID)
    if err != nil {
        return err
    }

    blocks, err := w.client.GetBlocks(ctx, pageID)
    if err != nil {
        return err
    }

    // 2. Convert to markdown - NO LOCK (pure computation)
    markdown := w.converter.Convert(page, blocks)

    // 3. Write files - LOCK (protect filesystem)
    w.store.Lock()
    defer w.store.Unlock()

    if err := w.store.Write(ctx, path, markdown); err != nil {
        return err
    }

    if err := w.updateRegistry(ctx, pageID, path); err != nil {
        return err
    }

    return nil
}
```

### Benefits

1. **Non-blocking API calls**: Multiple pages can be fetched concurrently
2. **Fast lock duration**: Only held during quick file I/O
3. **Responsive webhooks**: New events can be queued while sync is fetching
4. **Better throughput**: API latency doesn't serialize all operations

### Signal Channel

Use a buffered channel of size 1 for notifications:

```go
notify: make(chan struct{}, 1)

func (w *SyncWorker) Notify() {
    select {
    case w.notify <- struct{}{}:
        // Signal sent
    default:
        // Already signaled, no need to queue another
    }
}
```

This pattern ensures:
- At most one pending signal at any time
- Non-blocking sends from webhook handler
- No missed signals (sync always checks queue after waking)

### Server Integration

Update the `serve` command to start the sync worker:

```go
func serveCommand() *cli.Command {
    // ...
    Action: func(ctx context.Context, cmd *cli.Command) error {
        // Setup store, queue manager, crawler...

        // Create sync worker
        syncWorker := webhook.NewSyncWorker(crawler, st, logger)

        // Start sync worker in background
        go func() {
            if err := syncWorker.Start(ctx); err != nil && err != context.Canceled {
                logger.Error("sync worker stopped", "error", err)
            }
        }()

        // Create webhook handler with sync worker
        handler := webhook.NewWebhookHandler(qm, st, secret, syncWorker, logger)

        // Start HTTP server...
    }
}
```

### Configuration

New environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_WEBHOOK_AUTO_SYNC` | `true` | Enable automatic sync on webhook events |
| `NTN_WEBHOOK_SYNC_DELAY` | `0` | Delay before processing (debounce rapid changes) |

### Graceful Shutdown

On shutdown:
1. Cancel the context
2. Wait for sync worker goroutine to exit
3. Optionally commit pending changes
4. Close HTTP server

```go
func (s *Server) Shutdown(ctx context.Context) error {
    // Signal shutdown to sync worker
    s.cancel()

    // Wait for sync worker to finish (respects context cancellation)
    <-s.syncWorkerDone

    // Final commit if there are pending changes
    s.store.Lock()
    s.commitPendingChanges(ctx)
    s.store.Unlock()

    // Shutdown HTTP server
    return s.httpServer.Shutdown(ctx)
}
```

## Implementation Tasks

- [ ] Add `Lock()`/`Unlock()` methods to `Store` interface and `LocalStore`
- [ ] Create `internal/webhook/worker.go` with SyncWorker
- [ ] Add Notify() calls to webhook handlers
- [ ] Implement parent validation before queuing pages
- [ ] Add minimal registry entries for untracked pages
- [ ] Update serve command to start sync worker
- [ ] Add graceful shutdown handling
- [ ] Add tests for concurrent access
- [ ] Add `NTN_WEBHOOK_SYNC_DELAY` debounce option

## Example Flow

1. User edits page in Notion
2. Notion sends `page.content_updated` webhook
3. **Webhook handler** (synchronous, fast):
   - Queues page with priority ID (e.g., 999)
   - Calls `syncWorker.Notify()`
   - Returns HTTP 200 to Notion
4. **Sync goroutine** (asynchronous):
   - Wakes up from notify signal
   - Reads queue entry 999
   - Fetches page from Notion API (no lock)
   - Converts to markdown (no lock)
   - Acquires lock, writes file, updates registry, releases lock
   - Queue is empty, waits for next signal

## Parent Validation

### Rule

When processing a webhook event, the page should only be fully synced if **at least one of its parents is already tracked** in the registry. This ensures we only sync pages within the user's configured hierarchy.

### Behavior

| Parent Status | Action |
|---------------|--------|
| At least one parent tracked | Full sync: fetch content, write markdown, update registry |
| No parent tracked | Partial: save page ID to registry only, skip content sync |

### Rationale

1. **Scope control**: Prevents syncing random pages from the workspace that aren't part of tracked hierarchies
2. **Future discovery**: By saving the ID, if a parent is added later, we can discover and sync these pages
3. **Resource efficiency**: Avoids fetching content for pages outside the sync scope

### Implementation

```go
func (w *SyncWorker) shouldSyncPage(ctx context.Context, pageID string, parent *Parent) bool {
    // Check if page itself is already tracked (e.g., it's a root page)
    if w.isPageTracked(ctx, pageID) {
        return true
    }

    // Check if parent is tracked
    if parent != nil && parent.ID != "" {
        if w.isPageTracked(ctx, parent.ID) {
            return true
        }
        // Recursively check parent's parents via API if needed
    }

    return false
}

func (h *WebhookHandler) handlePageChange(ctx context.Context, event WebhookEvent) {
    pageID := event.GetEntityID()

    // Always register the page ID (for future discovery)
    h.registerPageID(ctx, pageID, event.Data.Parent)

    // Only queue for full sync if parent is tracked
    if !h.shouldSyncPage(ctx, pageID, event.Data.Parent) {
        h.logger.InfoContext(ctx, "skipping page sync - no tracked parent",
            "page_id", pageID,
            "parent_id", event.Data.Parent.ID)
        return
    }

    // Queue for full sync
    h.queueManager.CreateWebhookEntry(ctx, pageID, folder)
}
```

### Registry Entry for Untracked Pages

Pages without tracked parents get a minimal registry entry:

```json
{
  "page_id": "abc123",
  "parent_id": "xyz789",
  "parent_type": "page",
  "discovered_at": "2026-01-24T10:30:00Z",
  "synced": false
}
```

When a parent is later added via `ntnsync add`, these discovered pages can be queued for sync.

---

## Edge Cases

### Rapid Updates

Multiple rapid updates to the same page:
- Each webhook queues an entry
- Sync worker processes them in order
- Later syncs may be redundant but harmless
- Consider deduplication in queue or debounce delay

### Sync Errors

If sync fails for a page:
- Log the error
- Continue processing other queue items
- Failed items remain in queue for retry
- Consider exponential backoff for repeated failures

### Long-Running Sync

If sync takes a long time:
- Webhook responses return immediately (async processing)
- New webhooks queue normally
- Sync worker processes everything when current operation completes
