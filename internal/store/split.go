package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const metadataPrefix = ".notion-sync"

// SplitStore routes operations by path prefix.
// Paths starting with ".notion-sync" go to metadataStore,
// everything else goes to contentStore.
type SplitStore struct {
	contentStore  *LocalStore
	metadataStore *LocalStore
}

// NewSplitStore creates a new SplitStore that routes metadata operations
// to a separate store.
func NewSplitStore(contentStore, metadataStore *LocalStore) *SplitStore {
	return &SplitStore{
		contentStore:  contentStore,
		metadataStore: metadataStore,
	}
}

func isMetadataPath(path string) bool {
	return strings.HasPrefix(path, metadataPrefix)
}

func (s *SplitStore) storeFor(path string) *LocalStore {
	if isMetadataPath(path) {
		return s.metadataStore
	}
	return s.contentStore
}

// Read reads a file from the appropriate store.
func (s *SplitStore) Read(ctx context.Context, path string) ([]byte, error) {
	return s.storeFor(path).Read(ctx, path)
}

// Exists checks if a file exists in the appropriate store.
func (s *SplitStore) Exists(ctx context.Context, path string) (bool, error) {
	return s.storeFor(path).Exists(ctx, path)
}

// List lists files in the appropriate store.
func (s *SplitStore) List(ctx context.Context, dir string) ([]FileInfo, error) {
	return s.storeFor(dir).List(ctx, dir)
}

// BeginTx starts a split transaction that routes writes to the correct store.
func (s *SplitStore) BeginTx(ctx context.Context) (Transaction, error) {
	contentTx, err := s.contentStore.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin content tx: %w", err)
	}
	metadataTx, err := s.metadataStore.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin metadata tx: %w", err)
	}
	return &splitTransaction{
		contentTx:  contentTx,
		metadataTx: metadataTx,
	}, nil
}

// Push pushes both stores to their respective remotes.
func (s *SplitStore) Push(ctx context.Context) error {
	if err := s.contentStore.Push(ctx); err != nil {
		return fmt.Errorf("push content: %w", err)
	}
	if err := s.metadataStore.Push(ctx); err != nil {
		return fmt.Errorf("push metadata: %w", err)
	}
	return nil
}

// Lock acquires locks on both stores (content first).
func (s *SplitStore) Lock() {
	s.contentStore.Lock()
	s.metadataStore.Lock()
}

// Unlock releases locks on both stores (metadata first to avoid deadlock).
func (s *SplitStore) Unlock() {
	s.metadataStore.Unlock()
	s.contentStore.Unlock()
}

// Pull fetches and merges changes from remote for both stores.
func (s *SplitStore) Pull(ctx context.Context) error {
	if err := s.contentStore.Pull(ctx); err != nil {
		return fmt.Errorf("pull content: %w", err)
	}
	if err := s.metadataStore.Pull(ctx); err != nil {
		return fmt.Errorf("pull metadata: %w", err)
	}
	return nil
}

// IsRemoteEnabled returns true if the content store has remote operations configured.
func (s *SplitStore) IsRemoteEnabled() bool {
	return s.contentStore.IsRemoteEnabled()
}

// RemoteConfig returns the content store's remote configuration.
func (s *SplitStore) RemoteConfig() *RemoteConfig {
	return s.contentStore.RemoteConfig()
}

// ContentStore returns the underlying content store.
func (s *SplitStore) ContentStore() *LocalStore {
	return s.contentStore
}

// MetadataStore returns the underlying metadata store.
func (s *SplitStore) MetadataStore() *LocalStore {
	return s.metadataStore
}

// splitTransaction routes write operations to the correct underlying transaction.
type splitTransaction struct {
	contentTx  Transaction
	metadataTx Transaction
}

func (t *splitTransaction) txFor(path string) Transaction {
	if isMetadataPath(path) {
		return t.metadataTx
	}
	return t.contentTx
}

// Write writes content to the appropriate transaction.
func (t *splitTransaction) Write(ctx context.Context, path string, content []byte) error {
	return t.txFor(path).Write(ctx, path, content)
}

// WriteStream writes streamed content to the appropriate transaction.
func (t *splitTransaction) WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error) {
	return t.txFor(path).WriteStream(ctx, path, reader)
}

// Delete deletes a file from the appropriate transaction.
func (t *splitTransaction) Delete(ctx context.Context, path string) error {
	return t.txFor(path).Delete(ctx, path)
}

// Mkdir creates a directory in the appropriate transaction.
func (t *splitTransaction) Mkdir(ctx context.Context, path string) error {
	return t.txFor(path).Mkdir(ctx, path)
}

// Commit commits both transactions.
func (t *splitTransaction) Commit(ctx context.Context, message string) error {
	if err := t.contentTx.Commit(ctx, message); err != nil {
		return fmt.Errorf("commit content: %w", err)
	}
	if err := t.metadataTx.Commit(ctx, "[metadata] "+message); err != nil {
		return fmt.Errorf("commit metadata: %w", err)
	}
	return nil
}

// Rollback rolls back both transactions.
func (t *splitTransaction) Rollback(ctx context.Context) error {
	return errors.Join(
		t.contentTx.Rollback(ctx),
		t.metadataTx.Rollback(ctx),
	)
}
