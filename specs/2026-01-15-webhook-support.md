# Webhook Support

**Date:** 2026-01-15
**Status:** Proposal

## Overview

### Problem

Real-time syncing requires responding to Notion changes as they happen. Currently, ntnsync only supports manual or scheduled pulls.

### Goal

Support webhook events to enable real-time syncing based on Notion changes.

### Design Principles

- **Asynchronous processing**: Acknowledge webhooks immediately, process in background
- **Signature verification**: Verify webhook authenticity when secret is configured (optional)
- **Timestamp validation**: Reject old events to prevent replay attacks (when signature verification is enabled)
- **Graceful degradation**: Log errors but don't fail on individual events
- **Context-aware logging**: Use structured logging with context for better traceability
- **Queue ID priority**: Webhook events use decrementing IDs (< 1000) for priority processing

---

## 1. Configuration

### Environment Variables

```bash
# Webhook server configuration
NTN_WEBHOOK_ENABLED=true
NTN_WEBHOOK_PORT=8080
NTN_WEBHOOK_SECRET=your_webhook_secret_here  # Optional: if not set, signature verification is skipped
NTN_WEBHOOK_PATH=/webhooks/notion

# Auto-sync on webhook events (default: true)
NTN_WEBHOOK_AUTO_SYNC=true
```

### Notion Integration Setup

In Notion integration settings:

1. **Webhooks**: Enable webhook support
2. **Webhook URL**: `https://your-domain.com/ntnsync/webhooks/notion`
3. **Webhook Secret**: Generate and store securely (optional, but recommended for production)
4. **Subscribe to events**:
   - `page.created`
   - `page.updated`
   - `page.deleted`
   - `data_source.created`
   - `data_source.updated`
   - `data_source.deleted`

### Supported Event Types

| Event | Description |
|-------|-------------|
| `page.created` | Page created |
| `page.updated` | Page updated |
| `page.deleted` | Page deleted |
| `data_source.created` | New data source added to a database |
| `data_source.updated` | Data source properties or name changed |
| `data_source.deleted` | Data source removed from database |

---

## 2. Webhook Server

### HTTP Handler

```go
type WebhookHandler struct {
    client        *notion.Client
    syncService   *SyncService
    logger        *slog.Logger
    webhookSecret string
}

func (h *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Verify webhook signature
    if !h.verifySignature(r) {
        h.logger.WarnContext(ctx, "Invalid webhook signature")
        http.Error(w, "Invalid signature", http.StatusUnauthorized)
        return
    }

    // Parse webhook payload
    var event WebhookEvent
    if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
        h.logger.ErrorContext(ctx, "Failed to decode webhook", "error", err)
        http.Error(w, "Invalid payload", http.StatusBadRequest)
        return
    }

    // Process event asynchronously
    go h.processEvent(context.Background(), event)

    // Acknowledge receipt immediately
    w.WriteHeader(http.StatusOK)
}
```

### Signature Verification

```go
func (h *WebhookHandler) verifySignature(r *http.Request) bool {
    // Skip verification if no secret is configured
    if h.webhookSecret == "" {
        return true
    }

    signature := r.Header.Get("Notion-Webhook-Signature")
    timestamp := r.Header.Get("Notion-Webhook-Timestamp")

    // Validate timestamp (reject events older than 5 minutes)
    if !h.validateTimestamp(timestamp) {
        return false
    }

    // Read body
    body, err := io.ReadAll(r.Body)
    if err != nil {
        return false
    }
    r.Body = io.NopCloser(bytes.NewBuffer(body))

    // Reconstruct signed content
    signedContent := timestamp + string(body)

    // Compute HMAC
    mac := hmac.New(sha256.New, []byte(h.webhookSecret))
    mac.Write([]byte(signedContent))
    expectedSignature := hex.EncodeToString(mac.Sum(nil))

    return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

func (h *WebhookHandler) validateTimestamp(timestamp string) bool {
    ts, err := strconv.ParseInt(timestamp, 10, 64)
    if err != nil {
        return false
    }

    // Reject events older than 5 minutes
    age := time.Since(time.Unix(ts, 0))
    return age < 5*time.Minute
}
```

### Event Router

```go
func (h *WebhookHandler) processEvent(ctx context.Context, event WebhookEvent) {
    h.logger.InfoContext(ctx, "Processing webhook event",
        "event_type", event.Type,
        "object_id", event.Data.ID,
    )

    switch event.Type {
    case "page.created", "page.updated":
        h.handlePageChange(ctx, event)
    case "page.deleted":
        h.handlePageDeletion(ctx, event)
    case "data_source.created", "data_source.updated":
        h.handleDataSourceChange(ctx, event)
    case "data_source.deleted":
        h.handleDataSourceDeletion(ctx, event)
    default:
        h.logger.WarnContext(ctx, "Unhandled event type", "type", event.Type)
    }
}
```

---

## 3. Event Handlers

### Page Change Handler

Handles `page.created` and `page.updated` events.

```go
func (h *WebhookHandler) handlePageChange(ctx context.Context, event WebhookEvent) {
    pageID := event.Data.ID

    // Extract parent information
    var dataSourceID, databaseID string
    if parent := event.Data.Parent; parent != nil {
        dataSourceID = parent.DataSourceID
        databaseID = parent.DatabaseID
    }

    h.logger.DebugContext(ctx, "Handling page change",
        "page_id", pageID,
        "data_source_id", dataSourceID,
        "database_id", databaseID,
    )

    // Fetch full page content from API
    page, err := h.client.GetPage(ctx, pageID)
    if err != nil {
        h.logger.ErrorContext(ctx, "Failed to fetch page", "error", err)
        return
    }

    // Sync the page to local filesystem
    if err := h.syncService.SyncPage(ctx, page); err != nil {
        h.logger.ErrorContext(ctx, "Failed to sync page", "error", err)
        return
    }

    h.logger.InfoContext(ctx, "Page synced successfully", "page_id", pageID)
}
```

### Page Deletion Handler

Handles `page.deleted` events.

```go
func (h *WebhookHandler) handlePageDeletion(ctx context.Context, event WebhookEvent) {
    pageID := event.Data.ID

    h.logger.DebugContext(ctx, "Handling page deletion", "page_id", pageID)

    // Look up page in registry
    registry, err := h.syncService.GetPageRegistry(pageID)
    if err != nil {
        h.logger.WarnContext(ctx, "Page not found in registry", "page_id", pageID)
        return
    }

    // Delete markdown file
    if err := os.Remove(registry.FilePath); err != nil && !os.IsNotExist(err) {
        h.logger.ErrorContext(ctx, "Failed to delete file", "error", err)
        return
    }

    // Delete registry entry
    if err := h.syncService.DeletePageRegistry(pageID); err != nil {
        h.logger.ErrorContext(ctx, "Failed to delete registry", "error", err)
        return
    }

    h.logger.InfoContext(ctx, "Page deleted successfully", "page_id", pageID)
}
```

### Data Source Change Handler

Handles `data_source.created` and `data_source.updated` events.

```go
func (h *WebhookHandler) handleDataSourceChange(ctx context.Context, event WebhookEvent) {
    dataSourceID := event.Data.ID

    h.logger.DebugContext(ctx, "Handling data source change",
        "data_source_id", dataSourceID,
        "event_type", event.Type,
    )

    // Fetch updated data source info
    dataSource, err := h.client.GetDataSource(ctx, dataSourceID)
    if err != nil {
        h.logger.ErrorContext(ctx, "Failed to fetch data source", "error", err)
        return
    }

    // Update database registry
    if err := h.syncService.UpdateDataSourceRegistry(ctx, dataSource); err != nil {
        h.logger.ErrorContext(ctx, "Failed to update registry", "error", err)
        return
    }

    // If new data source, sync all its pages
    if event.Type == "data_source.created" {
        if err := h.syncService.SyncDataSource(ctx, dataSourceID); err != nil {
            h.logger.ErrorContext(ctx, "Failed to sync new data source", "error", err)
            return
        }
    }

    h.logger.InfoContext(ctx, "Data source synced successfully", "data_source_id", dataSourceID)
}
```

---

## 4. Queue Integration

### Queue ID Priority System

Queue IDs determine processing order. Webhook events are prioritized by using IDs below the starting point.

**ID Ranges:**
- **Below 1000** (999, 998, 997...): Webhook-triggered events (high priority)
- **1000 and above** (1000, 1001, 1002...): Regular sync operations (normal priority)

**Processing order example:**
```
0997.json  <- Webhook event (processed first)
0998.json  <- Webhook event
0999.json  <- Webhook event
1000.json <- Regular sync
1001.json <- Regular sync
1002.json <- Regular sync (processed last)
```

### Webhook Queuing (Decrementing IDs)

```go
func (q *Queue) QueueFromWebhook(pageID string) (int, error) {
    // Find the current minimum queue ID
    minID := q.getMinQueueID()

    // If no entries exist below 1000, start at 999
    // Otherwise, decrement from the current minimum
    var newID int
    if minID >= 1000 || minID == 0 {
        newID = 999
    } else {
        newID = minID - 1
    }

    return newID, q.enqueue(newID, pageID)
}
```

### Regular Queuing (Incrementing IDs)

```go
func (q *Queue) Queue(pageID string) (int, error) {
    // Find the current maximum queue ID
    maxID := q.getMaxQueueID()

    // If no entries exist, start at 1000
    // Otherwise, increment from the current maximum
    var newID int
    if maxID < 1000 {
        newID = 1000
    } else {
        newID = maxID + 1
    }

    return newID, q.enqueue(newID, pageID)
}
```

### Queue Entry Format

Webhook events must use `type: update` (not `type: init`) to ensure pages are always re-synced.

```json
{
  "page_id": "abc123",
  "type": "update",
  "created_at": "2026-01-15T10:30:00Z"
}
```

### Update Condition

Only update if the webhook's `created_at` is after the page's `last_synced`:

```go
func (s *SyncService) shouldUpdate(queueEntry QueueEntry, pageRegistry PageRegistry) bool {
    // Always update if page has never been synced
    if pageRegistry.LastSynced.IsZero() {
        return true
    }

    // Update only if the webhook was received after the last sync
    return queueEntry.CreatedAt.After(pageRegistry.LastSynced)
}
```

### Page Registry Format

Page sync metadata stored in `.notion-sync/ids/page-{id}.json`:

```json
{
  "page_id": "abc123def456",
  "file_path": "docs/my-page.md",
  "last_synced": "2026-01-15T10:30:00Z",
  "notion_type": "page"
}
```

For databases:
```json
{
  "page_id": "xyz789ghi012",
  "file_path": "docs/my-database.md",
  "last_synced": "2026-01-15T10:30:00Z",
  "notion_type": "database"
}
```

---

## 5. Version Endpoint

The webhook server exposes a `/api/version` endpoint for health checks and deployment verification (unauthenticated).

### Version Package

```go
// internal/version/version.go
package version

var (
    Version = "dev"     // Set via ldflags (e.g., "0.1.0" or "0.1.0-5-g1234567")
    Commit  = "unknown" // Set via ldflags (short git SHA)
    GitTime = "unknown" // Set via ldflags (ISO 8601 UTC format)
)
```

### Build Configuration

**Makefile:**

```makefile
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TIME ?= $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")
LDFLAGS := -X 'github.com/fclairamb/ntnsync/internal/version.Version=$(VERSION)' \
           -X 'github.com/fclairamb/ntnsync/internal/version.Commit=$(COMMIT)' \
           -X 'github.com/fclairamb/ntnsync/internal/version.GitTime=$(GIT_TIME)'

build:
	go build -ldflags "$(LDFLAGS)" -o ./ntnsync .
```

**GoReleaser (.goreleaser.yml):**

```yaml
builds:
  - ldflags:
      - -X 'github.com/fclairamb/ntnsync/internal/version.Version={{ .Version }}'
      - -X 'github.com/fclairamb/ntnsync/internal/version.Commit={{ .ShortCommit }}'
      - -X 'github.com/fclairamb/ntnsync/internal/version.GitTime={{ .CommitDate }}'
```

### API Handler

```go
func (h *WebhookHandler) HandleVersion(w http.ResponseWriter, r *http.Request) {
    response := map[string]string{
        "version":    version.Version,
        "commit":     version.Commit,
        "build_time": version.GitTime,
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}
```

### Endpoint Registration

```go
// In server setup
mux.HandleFunc("/api/version", handler.HandleVersion)
mux.HandleFunc(webhookPath, handler.HandleWebhook)
```

### Response Format

**Request:** `GET /api/version`

**Response:**
```json
{
  "version": "0.1.0-5-g1234567",
  "commit": "1234567",
  "build_time": "2026-01-15T10:30:00Z"
}
```

### Startup Logging

```go
func main() {
    slog.Info("Starting ntnsync",
        "version", version.Version,
        "commit", version.Commit,
        "build_time", version.GitTime,
    )
    // ...
}
```

---

## 6. Testing

### Unit Test Example

```go
func TestWebhookHandler(t *testing.T) {
    handler := &WebhookHandler{
        client:        mockClient,
        syncService:   mockSyncService,
        logger:        slog.Default(),
        webhookSecret: "test-secret",
    }

    // Test page.created event
    event := WebhookEvent{
        Type: "page.created",
        Data: EventData{
            ID: "page-123",
            Parent: &Parent{
                Type:         "data_source_id",
                DataSourceID: "ds-abc",
                DatabaseID:   "db-xyz",
            },
        },
    }

    // Simulate webhook request
    body, _ := json.Marshal(event)
    req := httptest.NewRequest("POST", "/webhooks/notion", bytes.NewReader(body))

    // Add signature
    timestamp := strconv.FormatInt(time.Now().Unix(), 10)
    signature := computeSignature(timestamp, body, "test-secret")
    req.Header.Set("Notion-Webhook-Signature", signature)
    req.Header.Set("Notion-Webhook-Timestamp", timestamp)

    rr := httptest.NewRecorder()
    handler.HandleWebhook(rr, req)

    assert.Equal(t, http.StatusOK, rr.Code)
}
```

---

## 7. Implementation Tasks

### Phase 1: Foundation

- [ ] Create `internal/version/version.go` with Version, Commit, GitTime variables
- [ ] Update Makefile with ldflags for version injection
- [ ] Add `serve` command to start webhook server
- [ ] Implement basic HTTP server with configurable port

### Phase 2: Webhook Handler

- [ ] Define `WebhookEvent` and `EventData` structs
- [ ] Implement `HandleWebhook` HTTP handler
- [ ] Implement HMAC signature verification
- [ ] Implement timestamp validation (5-minute window)
- [ ] Add `/api/version` endpoint

### Phase 3: Event Processing

- [ ] Implement event router (`processEvent`)
- [ ] Implement `handlePageChange` for page.created/updated
- [ ] Implement `handlePageDeletion` for page.deleted
- [ ] Implement `handleDataSourceChange` for data_source.created/updated
- [ ] Implement `handleDataSourceDeletion` for data_source.deleted

### Phase 4: Queue Integration

- [ ] Add `QueueFromWebhook` method with decrementing IDs
- [ ] Update `Queue` method to start at 1000
- [ ] Add `created_at` field to queue entries
- [ ] Implement `shouldUpdate` condition based on timestamps
- [ ] Ensure webhook events use `type: update`

### Phase 5: Testing

- [ ] Unit tests for signature verification
- [ ] Unit tests for timestamp validation
- [ ] Unit tests for each event handler
- [ ] Unit tests for queue priority ordering
- [ ] Integration test with mock Notion webhook

### Phase 6: Documentation

- [ ] Update CLI docs with `serve` command
- [ ] Document environment variables
- [ ] Add deployment examples (Docker, Kubernetes)
- [ ] Document Notion integration setup steps
