package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
)

const testSecret = "test-webhook-secret" //nolint:gosec // test constant

// TestVerifySignature_Valid verifies that valid signatures pass verification.
func TestVerifySignature_Valid(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := []byte(`{"type":"page.updated","data":{"id":"test-page-id"}}`)
	signature := computeSignature(timestamp, body, testSecret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", signature)
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	if !handler.verifySignature(req) {
		t.Error("expected valid signature to pass verification")
	}
}

// TestVerifySignature_Invalid verifies that invalid signatures fail verification.
func TestVerifySignature_Invalid(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := []byte(`{"type":"page.updated","data":{"id":"test-page-id"}}`)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", "invalid-signature")
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	if handler.verifySignature(req) {
		t.Error("expected invalid signature to fail verification")
	}
}

// TestVerifySignature_NoSecret verifies that verification is skipped when no secret is configured.
func TestVerifySignature_NoSecret(t *testing.T) {
	t.Parallel()
	handler := createTestHandlerWithoutSecret(t)
	body := []byte(`{"type":"page.updated","data":{"id":"test-page-id"}}`)

	// No signature headers, but should pass because no secret is configured
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))

	if !handler.verifySignature(req) {
		t.Error("expected verification to pass when no secret is configured")
	}
}

// TestVerifySignature_MissingHeaders verifies that missing headers fail verification.
func TestVerifySignature_MissingHeaders(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	body := []byte(`{"type":"page.updated","data":{"id":"test-page-id"}}`)

	// Missing signature
	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))

	if handler.verifySignature(req) {
		t.Error("expected missing signature to fail verification")
	}

	// Missing timestamp
	req = httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", "some-signature")

	if handler.verifySignature(req) {
		t.Error("expected missing timestamp to fail verification")
	}
}

// TestValidateTimestamp_Valid verifies timestamps within the window pass.
func TestValidateTimestamp_Valid(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)

	// Current time
	now := time.Now().Unix()
	if !handler.validateTimestamp(strconv.FormatInt(now, 10)) {
		t.Error("expected current timestamp to be valid")
	}

	// 1 minute ago
	oneMinAgo := time.Now().Add(-1 * time.Minute).Unix()
	if !handler.validateTimestamp(strconv.FormatInt(oneMinAgo, 10)) {
		t.Error("expected timestamp from 1 minute ago to be valid")
	}

	// 4 minutes ago (just within the 5-minute window)
	fourMinAgo := time.Now().Add(-4 * time.Minute).Unix()
	if !handler.validateTimestamp(strconv.FormatInt(fourMinAgo, 10)) {
		t.Error("expected timestamp from 4 minutes ago to be valid")
	}
}

// TestValidateTimestamp_Expired verifies timestamps outside the window fail.
func TestValidateTimestamp_Expired(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)

	// 6 minutes ago (outside the 5-minute window)
	sixMinAgo := time.Now().Add(-6 * time.Minute).Unix()
	if handler.validateTimestamp(strconv.FormatInt(sixMinAgo, 10)) {
		t.Error("expected timestamp from 6 minutes ago to be invalid")
	}

	// 1 hour ago
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	if handler.validateTimestamp(strconv.FormatInt(oneHourAgo, 10)) {
		t.Error("expected timestamp from 1 hour ago to be invalid")
	}
}

// TestValidateTimestamp_Invalid verifies invalid timestamp formats fail.
func TestValidateTimestamp_Invalid(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)

	if handler.validateTimestamp("not-a-number") {
		t.Error("expected invalid timestamp format to fail")
	}

	if handler.validateTimestamp("") {
		t.Error("expected empty timestamp to fail")
	}
}

// TestHandleWebhook_Success verifies successful webhook handling.
func TestHandleWebhook_Success(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	event := Event{
		Type:   "page.content_updated",
		Entity: &Entity{ID: "test-page-id", Type: "page"},
		Data: EventData{
			Parent: &Parent{ID: "parent-id", Type: "workspace"},
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	signature := computeSignature(timestamp, body, testSecret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", signature)
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// TestHandleWebhook_InvalidSignature verifies rejection of invalid signatures.
func TestHandleWebhook_InvalidSignature(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	event := Event{
		Type:   "page.content_updated",
		Entity: &Entity{ID: "test-page-id", Type: "page"},
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", "wrong-signature")
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

// TestHandleWebhook_InvalidMethod verifies rejection of non-POST requests.
func TestHandleWebhook_InvalidMethod(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/notion", nil)
	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rr.Code)
	}
}

// TestHandleWebhook_InvalidPayload verifies rejection of malformed JSON.
func TestHandleWebhook_InvalidPayload(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	body := []byte(`{invalid json}`)
	signature := computeSignature(timestamp, body, testSecret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", signature)
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

// TestHandleVersion verifies the version endpoint.
func TestHandleVersion(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rr := httptest.NewRecorder()
	handler.HandleVersion(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if _, ok := response["version"]; !ok {
		t.Error("expected version field in response")
	}
	if _, ok := response["commit"]; !ok {
		t.Error("expected commit field in response")
	}
	if _, ok := response["build_time"]; !ok {
		t.Error("expected build_time field in response")
	}
}

// TestHandleWebhook_URLVerification verifies handling of URL verification requests.
func TestHandleWebhook_URLVerification(t *testing.T) {
	t.Parallel()
	handler := createTestHandlerWithoutSecret(t) // No secret for simpler test

	// Notion sends verification token without a type field
	event := Event{
		VerificationToken: "test-verification-token-12345",
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

// TestHandleWebhook_PageCreated verifies handling of page.created events.
func TestHandleWebhook_PageCreated(t *testing.T) {
	t.Parallel()
	handler := createTestHandler(t)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	event := Event{
		Type:   "page.created",
		Entity: &Entity{ID: "new-page-id", Type: "page"},
		Data: EventData{
			Parent: &Parent{
				ID:   "parent-db-id",
				Type: "database",
			},
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}
	signature := computeSignature(timestamp, body, testSecret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/notion", bytes.NewReader(body))
	req.Header.Set("Notion-Webhook-Signature", signature)
	req.Header.Set("Notion-Webhook-Timestamp", timestamp)

	rr := httptest.NewRecorder()
	handler.HandleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Give async processing time to complete
	time.Sleep(100 * time.Millisecond)

	// Verify queue entry was created
	ctx := context.Background()
	files, err := handler.queueManager.ListEntries(ctx)
	if err != nil {
		t.Fatalf("failed to list queue entries: %v", err)
	}

	if len(files) == 0 {
		t.Error("expected queue entry to be created")
	}
}

// computeSignature computes the HMAC-SHA256 signature for webhook verification.
//
//nolint:unparam // test helper with consistent test data
func computeSignature(timestamp string, body []byte, secret string) string {
	signedContent := timestamp + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedContent))
	return hex.EncodeToString(mac.Sum(nil))
}

// createTestHandlerWithoutSecret creates a Handler without a secret configured.
func createTestHandlerWithoutSecret(t *testing.T) *Handler {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "webhook_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			t.Logf("failed to remove temp dir: %v", rmErr)
		}
	})

	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create necessary directories
	queueDir := filepath.Join(tmpDir, ".notion-sync", "queue")
	if err := os.MkdirAll(queueDir, 0750); err != nil {
		t.Fatalf("failed to create queue dir: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	qm := queue.NewManager(st, logger)

	// No secret configured, no sync worker
	return NewHandler(qm, st, "", true, logger, nil, nil)
}

// createTestHandler creates a Handler with a test store.
func createTestHandler(t *testing.T) *Handler {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "webhook_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			t.Logf("failed to remove temp dir: %v", rmErr)
		}
	})

	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create necessary directories
	queueDir := filepath.Join(tmpDir, ".notion-sync", "queue")
	if err := os.MkdirAll(queueDir, 0750); err != nil {
		t.Fatalf("failed to create queue dir: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	qm := queue.NewManager(st, logger)

	return NewHandler(qm, st, testSecret, true, logger, nil, nil)
}
