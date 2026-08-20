package webhook

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/sync"
)

const (
	folderDashedID     = "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"
	folderNormalizedID = "388aa28b3ffb80b69e5bc6a0eeaebf64"
	folderName         = "tech"
	// pageUpdatedEvent avoids adding a third bare "page.updated" literal to the
	// package, which would trip goconst.
	pageUpdatedEvent = "page.updated"
)

// newFolderTestHandler builds a handler over a fresh store and returns the
// handler, the store and the store's root directory.
func newFolderTestHandler(t *testing.T) (*Handler, *store.LocalStore, string) {
	t.Helper()

	tmpDir := t.TempDir()
	for _, dir := range []string{".notion-sync/queue", ".notion-sync/ids"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return NewHandler(queue.NewManager(st, logger), st, "", true, logger, nil, nil), st, tmpDir
}

// TestLookupPageFolderSurvivesCompaction is the regression test for a migrated
// repository: once `reindex --compact` has deleted the per-page registry files,
// the webhook handler must still resolve the page's folder through the sharded
// index instead of silently falling back to "default".
func TestLookupPageFolderSurvivesCompaction(t *testing.T) {
	t.Parallel()

	handler, st, tmpDir := newFolderTestHandler(t)
	ctx := context.Background()

	legacyPath := filepath.Join(tmpDir, ".notion-sync/ids", "page-"+folderNormalizedID+".json")
	legacy := `{"id":"` + folderNormalizedID + `","type":"page","folder":"` + folderName + `",` +
		`"file_path":"` + folderName + `/wiki.md","title":"Wiki"}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	// Before the migration: resolved from the legacy file.
	before, err := handler.lookupPageFolder(ctx, folderDashedID)
	if err != nil {
		t.Fatalf("lookupPageFolder before compaction: %v", err)
	}
	if before != folderName {
		t.Errorf("folder before compaction = %q, want %q", before, folderName)
	}

	if compactErr := sync.NewCrawler(nil, st).Compact(ctx, false); compactErr != nil {
		t.Fatalf("Compact: %v", compactErr)
	}
	if _, statErr := os.Stat(legacyPath); !os.IsNotExist(statErr) {
		t.Fatalf("compaction must remove the legacy file, stat err = %v", statErr)
	}

	// After the migration: must still resolve, now from the shard.
	after, err := handler.lookupPageFolder(ctx, folderDashedID)
	if err != nil {
		t.Fatalf("lookupPageFolder after compaction: %v", err)
	}
	if after != folderName {
		t.Errorf("folder after compaction = %q, want %q (not the default fallback)", after, folderName)
	}
}

// TestHandlePageChangeUsesRegistryFolderAfterCompaction is the end-to-end
// version: the queue entry a webhook event produces must carry the page's real
// folder, not "default".
func TestHandlePageChangeUsesRegistryFolderAfterCompaction(t *testing.T) {
	t.Parallel()

	handler, st, tmpDir := newFolderTestHandler(t)
	ctx := context.Background()

	legacy := `{"id":"` + folderNormalizedID + `","type":"page","folder":"` + folderName + `",` +
		`"file_path":"` + folderName + `/wiki.md","title":"Wiki"}`
	legacyPath := filepath.Join(tmpDir, ".notion-sync/ids", "page-"+folderNormalizedID+".json")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}

	if err := sync.NewCrawler(nil, st).Compact(ctx, false); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	tx, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	handler.queueManager.SetTransaction(tx)

	handler.handlePageChange(ctx, &Event{
		Type:   pageUpdatedEvent,
		Entity: &Entity{ID: folderDashedID, Type: "page"},
	}, tx)

	files, err := handler.queueManager.ListEntries(ctx)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 queue entry, got %d", len(files))
	}

	entry, err := handler.queueManager.ReadEntry(ctx, files[0])
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if entry.Folder != folderName {
		t.Errorf("queue entry folder = %q, want %q", entry.Folder, folderName)
	}
}
