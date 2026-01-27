package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTransaction_WriteStream(t *testing.T) {
	t.Parallel()

	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	// Create a local store
	store, err := NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()

	// Create a transaction for all write operations
	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	t.Run("stream write to new file", func(t *testing.T) {
		t.Parallel()
		testWriteStreamNewFile(ctx, t, store, tx)
	})

	t.Run("stream write creates parent directories", func(t *testing.T) {
		t.Parallel()
		testWriteStreamCreatesParentDirs(ctx, t, store, tx)
	})

	t.Run("stream write has correct permissions", func(t *testing.T) {
		t.Parallel()
		testWriteStreamPermissions(ctx, t, tx, tmpDir)
	})

	t.Run("stream write is atomic", func(t *testing.T) {
		t.Parallel()
		testWriteStreamAtomic(ctx, t, tx, tmpDir)
	})
}

func testWriteStreamNewFile(ctx context.Context, t *testing.T, store *LocalStore, tx Transaction) {
	t.Helper()

	content := []byte("hello streaming world")
	reader := bytes.NewReader(content)

	written, err := tx.WriteStream(ctx, "test/stream.txt", reader)
	if err != nil {
		t.Fatalf("WriteStream failed: %v", err)
	}

	if written != int64(len(content)) {
		t.Errorf("expected %d bytes written, got %d", len(content), written)
	}

	// Verify content was written correctly
	data, err := store.Read(ctx, "test/stream.txt")
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}

	if !bytes.Equal(data, content) {
		t.Errorf("content mismatch: got %q, want %q", data, content)
	}
}

func testWriteStreamCreatesParentDirs(ctx context.Context, t *testing.T, store *LocalStore, tx Transaction) {
	t.Helper()

	content := []byte("nested content")
	reader := bytes.NewReader(content)

	_, err := tx.WriteStream(ctx, "deep/nested/path/file.txt", reader)
	if err != nil {
		t.Fatalf("WriteStream failed: %v", err)
	}

	// Verify file exists
	exists, err := store.Exists(ctx, "deep/nested/path/file.txt")
	if err != nil {
		t.Fatalf("Exists check failed: %v", err)
	}
	if !exists {
		t.Error("expected file to exist")
	}
}

func testWriteStreamPermissions(ctx context.Context, t *testing.T, tx Transaction, tmpDir string) {
	t.Helper()

	content := []byte("permission test")
	reader := bytes.NewReader(content)

	_, err := tx.WriteStream(ctx, "perm-test.txt", reader)
	if err != nil {
		t.Fatalf("WriteStream failed: %v", err)
	}

	fullPath := filepath.Join(tmpDir, "perm-test.txt")
	info, err := os.Stat(fullPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	// Check file permissions (0600 = rw-------)
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}
}

func testWriteStreamAtomic(ctx context.Context, t *testing.T, tx Transaction, tmpDir string) {
	t.Helper()

	content := []byte("atomic test")
	reader := bytes.NewReader(content)

	_, err := tx.WriteStream(ctx, "atomic.txt", reader)
	if err != nil {
		t.Fatalf("WriteStream failed: %v", err)
	}

	// Check that no temp files exist
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp") {
			t.Errorf("found leftover temp file: %s", entry.Name())
		}
	}
}
