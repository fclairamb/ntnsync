package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
)

// TestProcessQueue_MaxQueueFiles_DeletedFilesAreCounted verifies that fully processed
// (deleted) queue files are counted toward the maxQueueFiles limit.
// This was a bug where the counter was only incremented when files were updated,
// not when they were deleted after full processing.
func TestProcessQueue_MaxQueueFiles_DeletedFilesAreCounted(t *testing.T) {
	t.Parallel()

	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "sync_test_maxqueue")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	// Create store
	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create necessary directories
	for _, dir := range []string{".notion-sync/queue", ".notion-sync/ids", "test"} {
		if mkErr := os.MkdirAll(filepath.Join(tmpDir, dir), 0750); mkErr != nil {
			t.Fatalf("failed to create dir %s: %v", dir, mkErr)
		}
	}

	// Create transaction
	ctx := context.Background()
	tx, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	// Create queue manager with transaction
	qm := queue.NewManager(st, slog.Default())
	qm.SetTransaction(tx)

	// Create a page registry so the page appears to already exist and be up-to-date
	// This will cause it to be skipped when using new format entries
	pageID := "existingpage123"
	regPath := filepath.Join(tmpDir, ".notion-sync/ids", pageID+".json")
	// Use a future last_edited time so the page is considered up-to-date
	regContent := `{"id":"` + pageID + `","folder":"test","file_path":"test/existing.md","title":"Existing","last_edited":"2030-01-01T00:00:00Z","last_synced":"2030-01-01T00:00:00Z"}`
	if writeErr := os.WriteFile(regPath, []byte(regContent), 0600); writeErr != nil {
		t.Fatalf("failed to write registry: %v", writeErr)
	}

	// Create 3 queue files using new format (Pages field) with old last_edited time
	// They will be skipped (page already up-to-date) and the queue files deleted
	// New format doesn't call the Notion API for skip check
	for range 3 {
		entry := queue.Entry{
			Type:   "init",
			Folder: "test",
			Pages: []queue.Page{{
				ID:         pageID,
				LastEdited: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), // Old time, will be skipped
			}},
		}
		if _, createErr := qm.CreateEntry(ctx, entry); createErr != nil {
			t.Fatalf("failed to create queue entry: %v", createErr)
		}
	}

	// Verify we have 3 queue files
	files, err := qm.ListEntries(ctx)
	if err != nil {
		t.Fatalf("failed to list entries: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 queue files, got %d", len(files))
	}

	// Create crawler without client (pages will be skipped due to registry, no API call needed)
	crawler := NewCrawler(nil, st, WithCrawlerLogger(slog.Default()))
	crawler.SetTransaction(tx)

	// Process with maxQueueFiles=1
	err = crawler.ProcessQueue(ctx, "", 0, 0, 1, 0)
	if err != nil {
		t.Fatalf("ProcessQueue failed: %v", err)
	}

	// Check remaining queue files
	// With maxQueueFiles=1, exactly 1 queue file should have been processed (and deleted)
	remainingFiles, err := qm.ListEntries(ctx)
	if err != nil {
		t.Fatalf("failed to list remaining entries: %v", err)
	}

	// Before the fix: would process all 3 files because deleted files weren't counted
	// After the fix: should process exactly 1 file
	if len(remainingFiles) != 2 {
		t.Errorf("expected 2 remaining queue files (1 should have been processed and deleted), got %d", len(remainingFiles))
	}
}
