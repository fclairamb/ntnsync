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
	Write(ctx context.Context, path string, content []byte) error
	WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error)
	Delete(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) error

	// Commit creates a git commit with all changes made in this transaction.
	// After commit, the transaction can continue to be used for more changes.
	Commit(ctx context.Context, message string) error

	// Rollback reverts all uncommitted changes and closes the transaction.
	Rollback(ctx context.Context) error
}

// ReadFSProvider returns an fs.FS view for read-only consumers.
type ReadFSProvider interface {
	FS() fs.FS
}

// RemoteStore is a Store that can synchronise with a remote repository.
// Every concrete store in this package implements it, which is what lets
// callers avoid type-switching on concrete store types.
type RemoteStore interface {
	Store

	// Pull refreshes the local view from the remote. It is a no-op when
	// remote operations are not enabled.
	Pull(ctx context.Context) error

	// IsRemoteEnabled reports whether remote operations are configured.
	IsRemoteEnabled() bool

	// RemoteConfig returns the remote configuration backing this store.
	RemoteConfig() *RemoteConfig
}

// WholeTreeChecker is implemented by stores that cannot serve operations
// requiring a walk of the entire repository tree. Stores that do not
// implement it are assumed to support every operation.
type WholeTreeChecker interface {
	// CheckWholeTreeSupported returns a non-nil error naming the storage mode
	// when the named command needs a whole-tree walk the store cannot provide.
	CheckWholeTreeSupported(command string) error
}
