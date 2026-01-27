package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/version"
)

const (
	// Maximum allowed timestamp age for webhook events (5 minutes).
	maxTimestampAge = 5 * time.Minute

	defaultFolderName = "default"
)

// Event represents a Notion webhook event payload.
type Event struct {
	ID                string    `json:"id"`                           // Event ID
	Type              string    `json:"type"`                         // Event type (e.g., "page.content_updated")
	Timestamp         string    `json:"timestamp,omitempty"`          // ISO 8601 timestamp
	WorkspaceID       string    `json:"workspace_id,omitempty"`       // Workspace ID
	WorkspaceName     string    `json:"workspace_name,omitempty"`     // Workspace name
	SubscriptionID    string    `json:"subscription_id,omitempty"`    // Webhook subscription ID
	IntegrationID     string    `json:"integration_id,omitempty"`     // Integration ID
	AttemptNumber     int       `json:"attempt_number,omitempty"`     // Delivery attempt number
	APIVersion        string    `json:"api_version,omitempty"`        // Notion API version
	Authors           []Author  `json:"authors,omitempty"`            // Users who triggered the event
	Entity            *Entity   `json:"entity,omitempty"`             // The affected entity (page, database, etc.)
	Data              EventData `json:"data"`                         // Event-specific data
	VerificationToken string    `json:"verification_token,omitempty"` // For URL verification requests
}

// Author represents a user who triggered the event.
type Author struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "person" or "bot"
}

// Entity represents the affected object (page, database, etc.).
type Entity struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "page", "database", etc.
}

// EventData contains event-specific details.
type EventData struct {
	Parent        *Parent        `json:"parent,omitempty"`
	UpdatedBlocks []UpdatedBlock `json:"updated_blocks,omitempty"` // For content_updated events
}

// Parent contains parent information.
type Parent struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "workspace", "page", "database", "team", etc.
}

// UpdatedBlock represents a block that was updated.
type UpdatedBlock struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// GetEntityID returns the entity ID from the event.
func (e *Event) GetEntityID() string {
	if e.Entity != nil {
		return e.Entity.ID
	}
	return ""
}

// GetEntityType returns the entity type from the event.
func (e *Event) GetEntityType() string {
	if e.Entity != nil {
		return e.Entity.Type
	}
	return ""
}

// Handler handles incoming webhook requests.
type Handler struct {
	queueManager *queue.Manager
	store        store.Store
	logger       *slog.Logger
	secret       string
	autoSync     bool
	syncWorker   *SyncWorker
	remoteConfig *store.RemoteConfig
}

// NewHandler creates a new webhook handler.
// If syncWorker is nil, automatic background sync is disabled.
func NewHandler(
	queueManager *queue.Manager,
	storeInst store.Store,
	secret string,
	autoSync bool,
	logger *slog.Logger,
	syncWorker *SyncWorker,
	remoteConfig *store.RemoteConfig,
) *Handler {
	return &Handler{
		queueManager: queueManager,
		store:        storeInst,
		logger:       logger,
		secret:       secret,
		autoSync:     autoSync,
		syncWorker:   syncWorker,
		remoteConfig: remoteConfig,
	}
}

// HandleWebhook handles incoming webhook requests.
func (h *Handler) HandleWebhook(writer http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	if req.Method != http.MethodPost {
		h.logger.WarnContext(ctx, "invalid method", "method", req.Method)
		http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify webhook signature
	if !h.verifySignature(req) {
		h.logger.WarnContext(ctx, "invalid webhook signature")
		http.Error(writer, "Invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse webhook payload
	var event Event
	rawJSON, err := io.ReadAll(req.Body)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to read webhook payload", "error", err)
		http.Error(writer, "Invalid payload", http.StatusBadRequest)
		return
	}

	h.logger.DebugContext(ctx, "received webhook", "rawJson", string(rawJSON))

	if err := json.Unmarshal(rawJSON, &event); err != nil {
		h.logger.ErrorContext(ctx, "failed to decode webhook", "error", err)
		http.Error(writer, "Invalid payload", http.StatusBadRequest)
		return
	}

	h.logger.InfoContext(ctx, "received webhook event",
		"event_type", event.Type,
		"entity_id", event.GetEntityID(),
		"entity_type", event.GetEntityType())

	// Process event asynchronously with a detached context
	// We use context.WithoutCancel to allow the goroutine to complete even if the request context is canceled
	go h.processEvent(context.WithoutCancel(ctx), &event)

	// Acknowledge receipt immediately
	writer.WriteHeader(http.StatusOK)
}

// handleURLVerification handles Notion's webhook URL verification request.
func (h *Handler) handleURLVerification(ctx context.Context, event *Event) {
	h.logger.InfoContext(ctx, "received URL verification request",
		"verification_token", event.VerificationToken)

	if event.VerificationToken == "" {
		h.logger.WarnContext(ctx, "URL verification request missing verification_token")
		return
	}
	h.logger.InfoContext(ctx, "URL verification successful")
}

// HandleVersion handles the /api/version endpoint.
func (h *Handler) HandleVersion(writer http.ResponseWriter, req *http.Request) {
	response := map[string]string{
		"version":    version.Version,
		"commit":     version.Commit,
		"build_time": version.GitTime,
	}

	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		h.logger.ErrorContext(req.Context(), "failed to encode version response", "error", err)
	}
}

// HandleHealth handles the /health endpoint for health checks.
func (h *Handler) HandleHealth(writer http.ResponseWriter, req *http.Request) {
	response := map[string]string{
		"status": "ok",
	}

	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		h.logger.ErrorContext(req.Context(), "failed to encode health response", "error", err)
	}
}

// verifySignature verifies the webhook signature using HMAC-SHA256.
// If no secret is configured, signature verification is skipped.
func (h *Handler) verifySignature(req *http.Request) bool {
	// Skip verification if no secret is configured
	if h.secret == "" {
		return true
	}

	signature := req.Header.Get("Notion-Webhook-Signature")
	timestamp := req.Header.Get("Notion-Webhook-Timestamp")

	if signature == "" || timestamp == "" {
		h.logger.Debug("missing signature or timestamp headers")
		return false
	}

	// Validate timestamp
	if !h.validateTimestamp(timestamp) {
		h.logger.Debug("timestamp validation failed", "timestamp", timestamp)
		return false
	}

	// Read body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		h.logger.Debug("failed to read body", "error", err)
		return false
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	// Reconstruct signed content: timestamp + body
	signedContent := timestamp + string(body)

	// Compute HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write([]byte(signedContent))
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

// validateTimestamp checks if the timestamp is within the allowed window.
func (h *Handler) validateTimestamp(timestamp string) bool {
	timestampValue, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	// Reject events older than maxTimestampAge
	age := time.Since(time.Unix(timestampValue, 0))
	return age < maxTimestampAge && age > -maxTimestampAge
}

// processEvent routes the event to the appropriate handler.
func (h *Handler) processEvent(ctx context.Context, event *Event) {
	h.logger.InfoContext(ctx, "processing webhook event",
		"event_type", event.Type,
		"entity_id", event.GetEntityID(),
		"entity_type", event.GetEntityType(),
		"workspace", event.WorkspaceName)

	// Create a transaction for write operations
	transaction, err := h.store.BeginTx(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to begin transaction", "error", err)
		return
	}
	h.queueManager.SetTransaction(transaction)

	switch event.Type {
	case "page.created", "page.updated", "page.content_updated", "page.properties_updated":
		h.handlePageChange(ctx, event, transaction)
	case "page.deleted", "page.undeleted":
		h.handlePageDeletion(ctx, event)
	case "database.created", "database.updated", "database.content_updated", "database.properties_updated":
		h.handleDatabaseChange(ctx, event, transaction)
	case "database.deleted", "database.undeleted":
		h.handleDatabaseDeletion(ctx, event)
	case "":
		if event.VerificationToken != "" {
			h.handleURLVerification(ctx, event)
		}
	default:
		h.logger.WarnContext(ctx, "unhandled event type", "type", event.Type)
	}
}

// handlePageChange handles page.created, page.updated, and page.content_updated events.
func (h *Handler) handlePageChange(ctx context.Context, event *Event, transaction store.Transaction) {
	pageID := event.GetEntityID()
	if pageID == "" {
		h.logger.WarnContext(ctx, "page change event missing entity ID")
		return
	}

	// Extract parent information for logging
	var parentID, parentType string
	if parent := event.Data.Parent; parent != nil {
		parentID = parent.ID
		parentType = parent.Type
	}

	h.logger.DebugContext(ctx, "handling page change",
		"page_id", pageID,
		"event_type", event.Type,
		"parent_id", parentID,
		"parent_type", parentType)

	// Look up the page's folder from registry
	folder, err := h.lookupPageFolder(ctx, pageID)
	if err != nil {
		h.logger.WarnContext(ctx, "page not found in registry, using default folder",
			"page_id", pageID,
			"error", err)
		folder = defaultFolderName
	}

	// Create webhook queue entry (uses decrementing IDs for priority)
	filename, err := h.queueManager.CreateWebhookEntry(ctx, pageID, folder)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to create queue entry",
			"page_id", pageID,
			"error", err)
		return
	}

	h.logger.InfoContext(ctx, "page queued for sync",
		"page_id", pageID,
		"queue_file", filename,
		"folder", folder)

	// Commit queue files immediately
	h.commitQueueFiles(ctx, transaction, "queued page "+pageID)

	// Notify sync worker if configured
	if h.syncWorker != nil {
		h.syncWorker.Notify()
	}
}

// handlePageDeletion handles page.deleted and page.undeleted events.
func (h *Handler) handlePageDeletion(ctx context.Context, event *Event) {
	pageID := event.GetEntityID()
	if pageID == "" {
		h.logger.WarnContext(ctx, "page deletion event missing entity ID")
		return
	}

	h.logger.DebugContext(ctx, "handling page deletion",
		"page_id", pageID,
		"event_type", event.Type)

	// For now, just log the deletion. Full implementation would:
	// 1. Look up the page in registry
	// 2. Delete the markdown file
	// 3. Delete the registry entry
	// This requires access to the sync package's registry methods

	h.logger.InfoContext(ctx, "page deletion event received (not yet implemented)",
		"page_id", pageID,
		"event_type", event.Type)
}

// handleDatabaseChange handles database.* events.
func (h *Handler) handleDatabaseChange(ctx context.Context, event *Event, transaction store.Transaction) {
	databaseID := event.GetEntityID()
	if databaseID == "" {
		h.logger.WarnContext(ctx, "database change event missing entity ID")
		return
	}

	h.logger.DebugContext(ctx, "handling database change",
		"database_id", databaseID,
		"event_type", event.Type)

	// Look up the database's folder from registry
	folder, err := h.lookupPageFolder(ctx, databaseID)
	if err != nil {
		h.logger.WarnContext(ctx, "database not found in registry, using default folder",
			"database_id", databaseID,
			"error", err)
		folder = defaultFolderName
	}

	// Create webhook queue entry
	filename, err := h.queueManager.CreateWebhookEntry(ctx, databaseID, folder)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to create queue entry",
			"database_id", databaseID,
			"error", err)
		return
	}

	h.logger.InfoContext(ctx, "database queued for sync",
		"database_id", databaseID,
		"queue_file", filename,
		"folder", folder)

	// Commit queue files immediately
	h.commitQueueFiles(ctx, transaction, "queued database "+databaseID)

	// Notify sync worker if configured
	if h.syncWorker != nil {
		h.syncWorker.Notify()
	}
}

// handleDatabaseDeletion handles database.deleted and database.undeleted events.
func (h *Handler) handleDatabaseDeletion(ctx context.Context, event *Event) {
	databaseID := event.GetEntityID()
	if databaseID == "" {
		h.logger.WarnContext(ctx, "database deletion event missing entity ID")
		return
	}

	h.logger.DebugContext(ctx, "handling database deletion",
		"database_id", databaseID,
		"event_type", event.Type)

	h.logger.InfoContext(ctx, "database deletion event received (not yet implemented)",
		"database_id", databaseID,
		"event_type", event.Type)
}

// lookupPageFolder attempts to find the folder for a page from the registry.
func (h *Handler) lookupPageFolder(ctx context.Context, pageID string) (string, error) {
	// Read page registry to get folder
	// Registry files are at .notion-sync/ids/page-{id}.json
	registryPath := ".notion-sync/ids/page-" + pageID + ".json"

	data, err := h.store.Read(ctx, registryPath)
	if err != nil {
		return "", err
	}

	var registry struct {
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return "", err
	}

	if registry.Folder == "" {
		return "default", nil
	}
	return registry.Folder, nil
}

// commitQueueFiles commits webhook queue files to git immediately and pushes to remote.
// This ensures queue files are persisted before sync processing begins.
func (h *Handler) commitQueueFiles(ctx context.Context, transaction store.Transaction, description string) {
	// Only commit if remote config is available and commits are enabled
	if h.remoteConfig == nil || !h.remoteConfig.IsCommitEnabled() {
		return
	}

	h.logger.DebugContext(ctx, "committing webhook queue files", "description", description)

	message := "[ntnsync] webhook: " + description
	if err := transaction.Commit(ctx, message); err != nil {
		h.logger.WarnContext(ctx, "failed to commit queue files", "error", err)
		return
	}

	h.logger.InfoContext(ctx, "webhook queue files committed", "description", description)

	// Push to remote if enabled
	if h.remoteConfig.IsPushEnabled() {
		if err := h.store.Push(ctx); err != nil {
			h.logger.WarnContext(ctx, "failed to push queue files", "error", err)
			return
		}
		h.logger.InfoContext(ctx, "webhook queue files pushed", "description", description)
	}
}
