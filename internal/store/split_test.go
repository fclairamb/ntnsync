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

	metadataDir, err := os.MkdirTemp("", "split-metadata-*")
	if err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(metadataDir) })

	contentStore, err := NewLocalStore(contentDir)
	if err != nil {
		t.Fatalf("failed to create content store: %v", err)
	}

	metadataStore, err := NewLocalStore(metadataDir)
	if err != nil {
		t.Fatalf("failed to create metadata store: %v", err)
	}

	split := NewSplitStore(contentStore, metadataStore)
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

	// Should NOT find it via metadata store
	_, err = split.metadataStore.Read(ctx, "docs/page.md")
	if err == nil {
		t.Error("expected error reading content path from metadata store")
	}
}

func TestSplitStore_RoutesMetadataReadsToMetadataStore(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	// Write metadata via metadata store's tx
	tx, err := split.metadataStore.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/state.json", []byte(`{"version":3}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read via SplitStore should find it
	data, err := split.Read(ctx, ".notion-sync/state.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != `{"version":3}` {
		t.Errorf("got %q, want %q", data, `{"version":3}`)
	}

	// Should NOT find it via content store
	_, err = split.contentStore.Read(ctx, ".notion-sync/state.json")
	if err == nil {
		t.Error("expected error reading metadata path from content store")
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

	// Write metadata
	if err = tx.Write(ctx, ".notion-sync/queue/00001000.json", []byte(`{"type":"update"}`)); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	// Verify routing: content in content store, metadata in metadata store
	contentData, err := split.contentStore.Read(ctx, "tech/page.md")
	if err != nil {
		t.Fatalf("read content from content store: %v", err)
	}
	if string(contentData) != "content" {
		t.Errorf("content store: got %q, want %q", contentData, "content")
	}

	metadataData, err := split.metadataStore.Read(ctx, ".notion-sync/queue/00001000.json")
	if err != nil {
		t.Fatalf("read metadata from metadata store: %v", err)
	}
	if string(metadataData) != `{"type":"update"}` {
		t.Errorf("metadata store: got %q, want %q", metadataData, `{"type":"update"}`)
	}
}

func TestSplitTransaction_CommitsBothStores(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write to both
	if err = tx.Write(ctx, "page.md", []byte("content")); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err = tx.Write(ctx, ".notion-sync/ids/page-abc.json", []byte(`{"id":"abc"}`)); err != nil {
		t.Fatalf("write metadata: %v", err)
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
	if err = initTx.Write(ctx, ".notion-sync/init.json", []byte("{}")); err != nil {
		t.Fatalf("write init metadata: %v", err)
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
	if err = tx.Write(ctx, ".notion-sync/ids/page-abc.json", []byte(`{"id":"abc"}`)); err != nil {
		t.Fatalf("write metadata: %v", err)
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

	// File should be gone from metadata store
	exists, err := split.metadataStore.Exists(ctx, ".notion-sync/queue/00001000.json")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Error("expected file to be deleted from metadata store")
	}
}

func TestSplitStore_ListRoutesCorrectly(t *testing.T) {
	t.Parallel()
	ctx, split := setupSplitStoreTest(t)

	tx, err := split.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Write queue files to metadata store
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

func TestIsMetadataPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path     string
		expected bool
	}{
		{".notion-sync/queue/00001000.json", true},
		{".notion-sync/ids/page-abc.json", true},
		{".notion-sync/state.json", true},
		{".notion-sync", true},
		{"tech/page.md", false},
		{"root.md", false},
		{"images/photo.png", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := isMetadataPath(tt.path); got != tt.expected {
			t.Errorf("isMetadataPath(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}
