package store

import (
	"context"
	"os"
	"testing"
)

func setupSplitStoreTest(t *testing.T) (context.Context, *SplitStore) {
	t.Helper()

	contentDir, err := os.MkdirTemp("", "split-content-*")
	if err != nil {
		t.Fatalf("failed to create content dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(contentDir) })

	queueDir, err := os.MkdirTemp("", "split-queue-*")
	if err != nil {
		t.Fatalf("failed to create queue dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(queueDir) })

	contentStore, err := NewLocalStore(contentDir)
	if err != nil {
		t.Fatalf("failed to create content store: %v", err)
	}

	queueStore, err := NewLocalStore(queueDir)
	if err != nil {
		t.Fatalf("failed to create queue store: %v", err)
	}

	split := NewSplitStore(contentStore, queueStore)
	return context.Background(), split
}

func TestSplitStore_RoutesContentReadsToContentStore(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	// Write content via content store's tx
	tx, err := split.contentStore.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err = tx.Write(ctx, "docs/page.md", []byte("# Hello")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read via SplitStore should find it
	data, err := split.Read(ctx, "docs/page.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "# Hello" {
		t.Errorf("got %q, want %q", data, "# Hello")
	}

	// Should NOT find it via queue store
	_, err = split.queueStore.Read(ctx, "docs/page.md")
	if err == nil {
		t.Error("expected error reading content path from queue store")
	}
}

func TestSplitStore_RoutesQueueReadsToQueueStore(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	// Write a queue file via queue store's tx
	tx, err := split.queueStore.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read via SplitStore should find it
	data, err := split.Read(ctx, ".notion-sync/queue/00001000.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != `{"type":"update"}` {
		t.Errorf("got %q, want %q", data, `{"type":"update"}`)
	}

	// Should NOT find it via content store
	_, err = split.contentStore.Read(ctx, ".notion-sync/queue/00001000.json")
	if err == nil {
		t.Error("expected error reading queue path from content store")
	}
}

// ids and state.json are metadata but must stay on the content (main) branch.
func TestSplitStore_RoutesIdsAndStateToContentStore(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/ids/page-abc.json", []byte(`{"id":"abc"}`)); err != nil {
		t.Fatalf("write ids: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/state.json", []byte(`{"version":3}`)); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// Both should be found in the content store, not the queue store.
	if _, err = split.contentStore.Read(ctx, ".notion-sync/ids/page-abc.json"); err != nil {
		t.Errorf("expected ids in content store: %v", err)
	}
	if _, err = split.contentStore.Read(ctx, ".notion-sync/state.json"); err != nil {
		t.Errorf("expected state in content store: %v", err)
	}
	if _, err = split.queueStore.Read(ctx, ".notion-sync/ids/page-abc.json"); err == nil {
		t.Error("expected ids NOT in queue store")
	}
	if _, err = split.queueStore.Read(ctx, ".notion-sync/state.json"); err == nil {
		t.Error("expected state NOT in queue store")
	}
}

func TestSplitTransaction_RoutesWritesByPath(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write content
	if err = tx.Write(ctx, "tech/page.md", []byte("content")); err != nil {
		t.Fatalf("write content: %v", err)
	}

	// Write queue file
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	// Verify routing: content in content store, queue in queue store
	contentData, err := split.contentStore.Read(ctx, "tech/page.md")
	if err != nil {
		t.Fatalf("read content from content store: %v", err)
	}
	if string(contentData) != "content" {
		t.Errorf("content store: got %q, want %q", contentData, "content")
	}

	queueData, err := split.queueStore.Read(ctx, ".notion-sync/queue/00001000.json")
	if err != nil {
		t.Fatalf("read queue from queue store: %v", err)
	}
	if string(queueData) != `{"type":"update"}` {
		t.Errorf("queue store: got %q, want %q", queueData, `{"type":"update"}`)
	}
}

func TestSplitTransaction_CommitsBothStores(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write to both: content/ids to main, queue to queue branch.
	if err = tx.Write(ctx, ".notion-sync/ids/page-abc.json", []byte(`{"id":"abc"}`)); err != nil {
		t.Fatalf("write ids: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	// Commit should not error
	if err = tx.Commit(ctx, "test commit"); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestSplitTransaction_RollbackBothStores(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	// Create initial commits so rollback has a HEAD to reset to
	initTx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin init tx: %v", err)
	}
	if err = initTx.Write(ctx, "init.md", []byte("init")); err != nil {
		t.Fatalf("write init content: %v", err)
	}
	if err = initTx.Write(ctx, ".notion-sync/queue/00000001.json", []byte("{}")); err != nil {
		t.Fatalf("write init queue: %v", err)
	}
	if err = initTx.Commit(ctx, "initial commit"); err != nil {
		t.Fatalf("commit init: %v", err)
	}

	// Now begin a new transaction and write to both
	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	if err = tx.Write(ctx, "page.md", []byte("content")); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	// Rollback should not error
	if err = tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestSplitTransaction_DeleteRoutesCorrectly(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write then delete a queue file
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err = tx.Delete(ctx, ".notion-sync/queue/00001000.json"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// File should be gone from queue store
	exists, err := split.queueStore.Exists(ctx, ".notion-sync/queue/00001000.json")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Error("expected file to be deleted from queue store")
	}
}

func TestSplitStore_ListRoutesCorrectly(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write queue files to queue store
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/queue/00001001.json", []byte(`{}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Write content file
	if err = tx.Write(ctx, "tech/page.md", []byte("content")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// List queue directory should return the queue files
	files, err := split.List(ctx, ".notion-sync/queue")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 queue files, got %d", len(files))
	}

	// List content directory should return content files
	files, err = split.List(ctx, "tech")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 content file, got %d", len(files))
	}
}

func TestIsQueuePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path     string
		expected bool
	}{
		{".notion-sync/queue/00001000.json", true},
		{".notion-sync/queue", true},
		{".notion-sync/ids/page-abc.json", false},
		{".notion-sync/state.json", false},
		{".notion-sync", false},
		{".notion-sync/queueing/x.json", false},
		{"tech/page.md", false},
		{"root.md", false},
		{"images/photo.png", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := isQueuePath(tt.path); got != tt.expected {
			t.Errorf("isQueuePath(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}
