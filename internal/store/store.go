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

// Store abstracts read/write file operations.
//
//nolint:interfacebloat // Store needs all these methods for complete file/git operations
type Store interface {
	// Read operations
	Read(ctx context.Context, path string) ([]byte, error)
	Exists(ctx context.Context, path string) (bool, error)
	List(ctx context.Context, dir string) ([]FileInfo, error)

	// Write operations
	Write(ctx context.Context, path string, content []byte) error
	WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error)
	Delete(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) error

	// Atomic batch operations (maps to git commits)
	BeginTx(ctx context.Context) (Transaction, error)

	// Remote operations
	Push(ctx context.Context) error

	// Concurrency control for external coordination (e.g., sync worker)
	Lock()
	Unlock()
}

// Transaction groups multiple operations into one commit.
type Transaction interface {
	Write(path string, content []byte) error
	Delete(path string) error
	Commit(message string) error
	Rollback() error
}

// ReadFSProvider returns an fs.FS view for read-only consumers.
type ReadFSProvider interface {
	FS() fs.FS
}
