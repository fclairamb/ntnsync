// Package store provides abstractions for file storage operations.
package store

import (
	"context"
	"io"
	"io/fs"
	"time"
)

// FileInfo represents file metadata.
type FileInfo struct {
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// Store abstracts file storage with transactional write operations.
type Store interface {
	// Read operations
	Read(ctx context.Context, path string) ([]byte, error)
	Exists(ctx context.Context, path string) (bool, error)
	List(ctx context.Context, dir string) ([]FileInfo, error)

	// Transaction management - all writes go through transactions
	BeginTx(ctx context.Context) (Transaction, error)

	// Remote operations
	Push(ctx context.Context) error

	// Concurrency control for external coordination (e.g., sync worker)
	Lock()
	Unlock()
}

// Transaction groups multiple write operations.
// All writes are applied immediately to the filesystem.
// Commit creates a git commit with all changes. Rollback reverts uncommitted changes.
type Transaction interface {
	// Write operations - applied immediately to filesystem
	Write(path string, content []byte) error
	WriteStream(path string, reader io.Reader) (int64, error)
	Delete(path string) error
	Mkdir(path string) error

	// Commit creates a git commit with all changes made in this transaction.
	// After commit, the transaction can continue to be used for more changes.
	Commit(message string) error

	// Rollback reverts all uncommitted changes and closes the transaction.
	Rollback() error
}

// ReadFSProvider returns an fs.FS view for read-only consumers.
type ReadFSProvider interface {
	FS() fs.FS
}
