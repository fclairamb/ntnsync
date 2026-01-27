package queue

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/ntnsync/internal/store"
)

const (
	testQueueFile    = "00000999.json"
	testQueueTypeUpd = "update"
)

// TestQueue_StartsAt1000 verifies that regular queue entries start at ID 1000.
func TestQueue_StartsAt1000(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Get the first queue number (should be 1000)
	num, err := qm.GetNextQueueNumber(ctx)
	if err != nil {
		t.Fatalf("GetNextQueueNumber failed: %v", err)
	}

	if num != webhookIDThreshold {
		t.Errorf("expected first queue number to be %d, got %d", webhookIDThreshold, num)
	}
}

// TestQueue_IncrementingIDs verifies that regular queue entries increment properly.
func TestQueue_IncrementingIDs(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Create first regular entry
	entry1 := Entry{
		Type:   "init",
		Folder: "test",
		Pages:  []Page{{ID: "page1"}},
	}
	filename1, err := qm.CreateEntry(ctx, entry1)
	if err != nil {
		t.Fatalf("CreateEntry failed: %v", err)
	}
	if filename1 != "00001000.json" {
		t.Errorf("expected first entry filename to be 00001000.json, got %s", filename1)
	}

	// Create second regular entry
	entry2 := Entry{
		Type:   "init",
		Folder: "test",
		Pages:  []Page{{ID: "page2"}},
	}
	filename2, err := qm.CreateEntry(ctx, entry2)
	if err != nil {
		t.Fatalf("CreateEntry failed: %v", err)
	}
	if filename2 != "00001001.json" {
		t.Errorf("expected second entry filename to be 00001001.json, got %s", filename2)
	}
}

// TestQueueFromWebhook_FirstEntry verifies the first webhook entry gets ID 999.
func TestQueueFromWebhook_FirstEntry(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Create first webhook entry
	filename, err := qm.CreateWebhookEntry(ctx, "page1", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry failed: %v", err)
	}

	if filename != testQueueFile {
		t.Errorf("expected first webhook entry filename to be 00000999.json, got %s", filename)
	}

	// Verify the entry was created correctly
	entry, err := qm.ReadEntry(ctx, filename)
	if err != nil {
		t.Fatalf("ReadEntry failed: %v", err)
	}
	if entry.Type != testQueueTypeUpd {
		t.Errorf("expected entry type to be 'update', got %s", entry.Type)
	}
	if entry.Folder != "test" {
		t.Errorf("expected entry folder to be 'test', got %s", entry.Folder)
	}
	if len(entry.Pages) != 1 || entry.Pages[0].ID != "page1" {
		t.Errorf("expected entry to have one page with ID 'page1', got %v", entry.Pages)
	}
}

// TestQueueFromWebhook_Decrementing verifies webhook entries decrement properly.
func TestQueueFromWebhook_Decrementing(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Create first webhook entry (999)
	filename1, err := qm.CreateWebhookEntry(ctx, "page1", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry 1 failed: %v", err)
	}
	if filename1 != testQueueFile {
		t.Errorf("expected first webhook entry to be 00000999.json, got %s", filename1)
	}

	// Create second webhook entry (998)
	filename2, err := qm.CreateWebhookEntry(ctx, "page2", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry 2 failed: %v", err)
	}
	if filename2 != "00000998.json" {
		t.Errorf("expected second webhook entry to be 00000998.json, got %s", filename2)
	}

	// Create third webhook entry (997)
	filename3, err := qm.CreateWebhookEntry(ctx, "page3", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry 3 failed: %v", err)
	}
	if filename3 != "00000997.json" {
		t.Errorf("expected third webhook entry to be 00000997.json, got %s", filename3)
	}
}

// TestQueueOrdering verifies webhook entries are processed before regular entries.
func TestQueueOrdering(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Create a regular entry first (should get ID 1000)
	regularEntry := Entry{
		Type:   "init",
		Folder: "test",
		Pages:  []Page{{ID: "regular1"}},
	}
	_, err := qm.CreateEntry(ctx, regularEntry)
	if err != nil {
		t.Fatalf("CreateEntry failed: %v", err)
	}

	// Create webhook entries (should get IDs 999, 998)
	_, err = qm.CreateWebhookEntry(ctx, "webhook1", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry 1 failed: %v", err)
	}
	_, err = qm.CreateWebhookEntry(ctx, "webhook2", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry 2 failed: %v", err)
	}

	// List entries (should be sorted: 998, 999, 1000)
	files, err := qm.ListEntries(ctx)
	if err != nil {
		t.Fatalf("ListEntries failed: %v", err)
	}

	expected := []string{"00000998.json", testQueueFile, "00001000.json"}
	if len(files) != len(expected) {
		t.Fatalf("expected %d entries, got %d: %v", len(expected), len(files), files)
	}

	for i, filename := range files {
		if filename != expected[i] {
			t.Errorf("expected files[%d] to be %s, got %s", i, expected[i], filename)
		}
	}

	// Verify that reading entries in order gives webhook entries first
	entry1, _ := qm.ReadEntry(ctx, files[0])
	if entry1.Pages[0].ID != "webhook2" {
		t.Errorf("expected first entry to be webhook2, got %s", entry1.Pages[0].ID)
	}

	entry2, _ := qm.ReadEntry(ctx, files[1])
	if entry2.Pages[0].ID != "webhook1" {
		t.Errorf("expected second entry to be webhook1, got %s", entry2.Pages[0].ID)
	}

	entry3, _ := qm.ReadEntry(ctx, files[2])
	if entry3.Pages[0].ID != "regular1" {
		t.Errorf("expected third entry to be regular1, got %s", entry3.Pages[0].ID)
	}
}

// TestGetMinQueueID verifies GetMinQueueID returns correct values.
func TestGetMinQueueID(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Empty queue should return 0
	minID, err := qm.GetMinQueueID(ctx)
	if err != nil {
		t.Fatalf("GetMinQueueID failed: %v", err)
	}
	if minID != 0 {
		t.Errorf("expected min ID to be 0 for empty queue, got %d", minID)
	}

	// Add regular entry (1000)
	regularEntry := Entry{
		Type:   "init",
		Folder: "test",
		Pages:  []Page{{ID: "page1"}},
	}
	_, err = qm.CreateEntry(ctx, regularEntry)
	if err != nil {
		t.Fatalf("CreateEntry failed: %v", err)
	}

	minID, err = qm.GetMinQueueID(ctx)
	if err != nil {
		t.Fatalf("GetMinQueueID failed: %v", err)
	}
	if minID != 1000 {
		t.Errorf("expected min ID to be 1000, got %d", minID)
	}

	// Add webhook entry (999)
	_, err = qm.CreateWebhookEntry(ctx, "page2", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry failed: %v", err)
	}

	minID, err = qm.GetMinQueueID(ctx)
	if err != nil {
		t.Fatalf("GetMinQueueID failed: %v", err)
	}
	if minID != 999 {
		t.Errorf("expected min ID to be 999, got %d", minID)
	}
}

// TestWebhookEntryWithExistingRegular verifies webhook entries work with existing regular entries.
func TestWebhookEntryWithExistingRegular(t *testing.T) {
	t.Parallel()
	_, qm := createTestStoreAndManager(t)
	ctx := context.Background()

	// Create regular entries first
	for range 3 {
		entry := Entry{
			Type:   "init",
			Folder: "test",
			Pages:  []Page{{ID: "regular"}},
		}
		_, err := qm.CreateEntry(ctx, entry)
		if err != nil {
			t.Fatalf("CreateEntry failed: %v", err)
		}
	}

	// Verify regular entries are at 1000, 1001, 1002
	files, _ := qm.ListEntries(ctx)
	if len(files) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(files))
	}
	if files[0] != "00001000.json" || files[2] != "00001002.json" {
		t.Errorf("unexpected regular entry filenames: %v", files)
	}

	// Add webhook entry (should get 999, not affect regular entries)
	webhookFile, err := qm.CreateWebhookEntry(ctx, "webhook1", "test")
	if err != nil {
		t.Fatalf("CreateWebhookEntry failed: %v", err)
	}
	if webhookFile != testQueueFile {
		t.Errorf("expected webhook entry to be 00000999.json, got %s", webhookFile)
	}

	// Verify next regular entry still gets 1003
	nextNum, err := qm.GetNextQueueNumber(ctx)
	if err != nil {
		t.Fatalf("GetNextQueueNumber failed: %v", err)
	}
	if nextNum != 1003 {
		t.Errorf("expected next queue number to be 1003, got %d", nextNum)
	}
}

// createTestStoreAndManager creates a temporary LocalStore and Manager with transaction for testing.
func createTestStoreAndManager(t *testing.T) (store.Store, *Manager) { //nolint:unparam // may be used in future
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "queue_test")
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

	// Create queue directory
	queuePath := filepath.Join(tmpDir, queueDir)
	if mkErr := os.MkdirAll(queuePath, 0750); mkErr != nil {
		t.Fatalf("failed to create queue dir: %v", mkErr)
	}

	// Create manager and set transaction
	qm := NewManager(st, slog.Default())
	tx, err := st.BeginTx(context.Background())
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	qm.SetTransaction(tx)

	return st, qm
}
