package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const queuePrefix = ".notion-sync/queue"

// SplitStore routes operations by path prefix.
// Paths under ".notion-sync/queue" go to queueStore (the queue branch),
// everything else — including content, ".notion-sync/ids", and
// ".notion-sync/state.json" — goes to contentStore (the main branch).
type SplitStore struct {
	contentStore *LocalStore
	queueStore   *LocalStore
}

// NewSplitStore creates a new SplitStore that routes queue operations
// to a separate store.
func NewSplitStore(contentStore, queueStore *LocalStore) *SplitStore {
	return &SplitStore{
		contentStore: contentStore,
		queueStore:   queueStore,
	}
}

func isQueuePath(path string) bool {
	return path == queuePrefix || strings.HasPrefix(path, queuePrefix+"/")
}

func (s *SplitStore) storeFor(path string) *LocalStore {
	if isQueuePath(path) {
		return s.queueStore
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
	queueTx, err := s.queueStore.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin queue tx: %w", err)
	}
	return &splitTransaction{
		contentTx: contentTx,
		queueTx:   queueTx,
	}, nil
}

// Push pushes both stores to their respective remotes.
func (s *SplitStore) Push(ctx context.Context) error {
	if err := s.contentStore.Push(ctx); err != nil {
		return fmt.Errorf("push content: %w", err)
	}
	if err := s.queueStore.Push(ctx); err != nil {
		return fmt.Errorf("push queue: %w", err)
	}
	return nil
}

// Lock acquires locks on both stores (content first).
func (s *SplitStore) Lock() {
	s.contentStore.Lock()
	s.queueStore.Lock()
}

// Unlock releases locks on both stores (queue first to avoid deadlock).
func (s *SplitStore) Unlock() {
	s.queueStore.Unlock()
	s.contentStore.Unlock()
}

// Pull fetches and merges changes from remote for both stores.
func (s *SplitStore) Pull(ctx context.Context) error {
	if err := s.contentStore.Pull(ctx); err != nil {
		return fmt.Errorf("pull content: %w", err)
	}
	if err := s.queueStore.Pull(ctx); err != nil {
		return fmt.Errorf("pull queue: %w", err)
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

// QueueStore returns the underlying queue store.
func (s *SplitStore) QueueStore() *LocalStore {
	return s.queueStore
}

// splitTransaction routes write operations to the correct underlying transaction.
type splitTransaction struct {
	contentTx Transaction
	queueTx   Transaction
}

func (t *splitTransaction) txFor(path string) Transaction {
	if isQueuePath(path) {
		return t.queueTx
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
	if err := t.queueTx.Commit(ctx, "[queue] "+message); err != nil {
		return fmt.Errorf("commit queue: %w", err)
	}
	return nil
}

// Rollback rolls back both transactions.
func (t *splitTransaction) Rollback(ctx context.Context) error {
	return errors.Join(
		t.contentTx.Rollback(ctx),
		t.queueTx.Rollback(ctx),
	)
}
