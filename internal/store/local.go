package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	msgRemoteRepoEmpty = "remote repository is empty"

	// File and directory permissions.
	dirPerm  = 0750 // Directory permissions: rwxr-x---
	filePerm = 0600 // File permissions: rw-------
)

// LocalStore implements Store using local filesystem and git.
type LocalStore struct {
	rootPath     string
	repo         *git.Repository
	mu           sync.RWMutex
	logger       *slog.Logger
	remoteConfig *RemoteConfig
}

// LocalStoreOption configures LocalStore.
type LocalStoreOption func(*LocalStore)

// WithLogger sets a custom logger for the store.
func WithLogger(l *slog.Logger) LocalStoreOption {
	return func(s *LocalStore) {
		s.logger = l
	}
}

// WithRemoteConfig sets the remote git configuration.
func WithRemoteConfig(cfg *RemoteConfig) LocalStoreOption {
	return func(s *LocalStore) {
		s.remoteConfig = cfg
	}
}

// NewLocalStore creates a new local store at the given path.
func NewLocalStore(path string, opts ...LocalStoreOption) (*LocalStore, error) {
	store := &LocalStore{
		rootPath: path,
		logger:   slog.Default(),
	}

	// Apply options first to get remote config
	for _, opt := range opts {
		opt(store)
	}

	// Initialize repository (clone from remote or init locally)
	repo, err := store.initializeRepository(path)
	if err != nil {
		return nil, err
	}

	store.repo = repo
	return store, nil
}

// Read reads a file from the store.
func (s *LocalStore) Read(ctx context.Context, path string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.logger.DebugContext(ctx, "reading file", "path", path)

	fullPath := filepath.Join(s.rootPath, path)
	data, err := os.ReadFile(fullPath) //nolint:gosec // path is application controlled
	if err != nil {
		s.logger.DebugContext(ctx, "read file failed", "path", path, "error", err)
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}

	s.logger.DebugContext(ctx, "read file complete", "path", path, "size", len(data))
	return data, nil
}

// Exists checks if a file exists.
func (s *LocalStore) Exists(ctx context.Context, path string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.logger.DebugContext(ctx, "checking file exists", "path", path)

	fullPath := filepath.Join(s.rootPath, path)
	_, err := os.Stat(fullPath)
	if err == nil {
		s.logger.DebugContext(ctx, "file exists", "path", path)
		return true, nil
	}
	if os.IsNotExist(err) {
		s.logger.DebugContext(ctx, "file does not exist", "path", path)
		return false, nil
	}
	s.logger.DebugContext(ctx, "exists check failed", "path", path, "error", err)
	return false, err
}

// List lists files in a directory.
func (s *LocalStore) List(ctx context.Context, dir string) ([]FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.logger.DebugContext(ctx, "listing directory", "dir", dir)

	fullPath := filepath.Join(s.rootPath, dir)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.DebugContext(ctx, "directory does not exist", "dir", dir)
			return nil, nil
		}
		s.logger.DebugContext(ctx, "list directory failed", "dir", dir, "error", err)
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Path:    filepath.Join(dir, entry.Name()),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	s.logger.DebugContext(ctx, "list directory complete", "dir", dir, "count", len(files))
	return files, nil
}

// Write writes content to a file.
func (s *LocalStore) Write(ctx context.Context, path string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.DebugContext(ctx, "writing file", "path", path, "size", len(content))

	fullPath := filepath.Join(s.rootPath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
		s.logger.DebugContext(ctx, "create parent dir failed", "path", path, "error", err)
		return fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, filePerm); err != nil {
		s.logger.DebugContext(ctx, "write file failed", "path", path, "error", err)
		return fmt.Errorf("write file %s: %w", path, err)
	}

	s.logger.DebugContext(ctx, "write file complete", "path", path)
	return nil
}

// Delete deletes a file.
func (s *LocalStore) Delete(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.DebugContext(ctx, "deleting file", "path", path)

	fullPath := filepath.Join(s.rootPath, path)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		s.logger.DebugContext(ctx, "delete file failed", "path", path, "error", err)
		return fmt.Errorf("delete file %s: %w", path, err)
	}

	s.logger.DebugContext(ctx, "delete file complete", "path", path)
	return nil
}

// Mkdir creates a directory.
func (s *LocalStore) Mkdir(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.DebugContext(ctx, "creating directory", "path", path)

	fullPath := filepath.Join(s.rootPath, path)
	if err := os.MkdirAll(fullPath, dirPerm); err != nil {
		s.logger.DebugContext(ctx, "create directory failed", "path", path, "error", err)
		return fmt.Errorf("create directory %s: %w", path, err)
	}

	s.logger.DebugContext(ctx, "create directory complete", "path", path)
	return nil
}

// BeginTx starts a new transaction.
func (s *LocalStore) BeginTx(_ context.Context) (Transaction, error) {
	return &localTransaction{
		store:   s,
		changes: nil,
	}, nil
}

// FS returns an fs.FS view of the store.
func (s *LocalStore) FS() fs.FS {
	return os.DirFS(s.rootPath)
}

// Lock acquires the store's write lock for external coordination.
func (s *LocalStore) Lock() {
	s.mu.Lock()
}

// Unlock releases the store's write lock.
func (s *LocalStore) Unlock() {
	s.mu.Unlock()
}

// IsRemoteEnabled returns true if remote git operations are configured.
func (s *LocalStore) IsRemoteEnabled() bool {
	return s.remoteConfig.IsEnabled()
}

// RemoteConfig returns the remote configuration.
func (s *LocalStore) RemoteConfig() *RemoteConfig {
	return s.remoteConfig
}

// Pull fetches and merges changes from the remote repository.
func (s *LocalStore) Pull(ctx context.Context) error {
	if !s.IsRemoteEnabled() {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	auth, err := s.remoteConfig.GetAuth()
	if err != nil {
		return fmt.Errorf("get auth: %w", err)
	}

	worktree, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	s.logger.InfoContext(ctx, "pulling from remote", "url", s.remoteConfig.URL, "branch", s.remoteConfig.Branch)

	err = worktree.PullContext(ctx, &git.PullOptions{
		RemoteName:    "origin",
		ReferenceName: plumbing.NewBranchReferenceName(s.remoteConfig.Branch),
		Auth:          auth,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			s.logger.InfoContext(ctx, "already up to date")
			return nil
		}
		// Handle empty remote repository
		if err.Error() == msgRemoteRepoEmpty {
			s.logger.InfoContext(ctx, msgRemoteRepoEmpty+", nothing to pull")
			return nil
		}
		return fmt.Errorf("pull: %w", err)
	}

	s.logger.InfoContext(ctx, "pull complete")
	return nil
}

// Push pushes local commits to the remote repository.
func (s *LocalStore) Push(ctx context.Context) error {
	if !s.IsRemoteEnabled() {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	auth, err := s.remoteConfig.GetAuth()
	if err != nil {
		return fmt.Errorf("get auth: %w", err)
	}

	s.logger.InfoContext(ctx, "pushing to remote", "url", s.remoteConfig.URL, "branch", s.remoteConfig.Branch)

	err = s.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			s.logger.InfoContext(ctx, "nothing to push")
			return nil
		}
		return fmt.Errorf("push: %w", err)
	}

	s.logger.InfoContext(ctx, "push complete")
	return nil
}

// TestConnection tests the connection to the remote repository.
func (s *LocalStore) TestConnection(ctx context.Context) error {
	if !s.IsRemoteEnabled() {
		return apperrors.ErrRemoteNotConfigured
	}

	auth, err := s.remoteConfig.GetAuth()
	if err != nil {
		return fmt.Errorf("get auth: %w", err)
	}

	// Try to list remote references to verify connectivity
	rem := git.NewRemote(nil, &config.RemoteConfig{
		Name: "origin",
		URLs: []string{s.remoteConfig.URL},
	})

	_, err = rem.ListContext(ctx, &git.ListOptions{
		Auth: auth,
	})
	if err != nil {
		return fmt.Errorf("list remote: %w", err)
	}

	return nil
}

// localTransaction implements Transaction.
type localTransaction struct {
	store     *LocalStore
	changes   []change
	mu        sync.Mutex
	committed bool
}

type change struct {
	path    string
	content []byte // nil means delete
}

// Write stages a file write.
func (t *localTransaction) Write(path string, content []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.committed {
		return apperrors.ErrTransactionCommitted
	}

	t.changes = append(t.changes, change{
		path:    path,
		content: content,
	})

	return nil
}

// Delete stages a file deletion.
func (t *localTransaction) Delete(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.committed {
		return apperrors.ErrTransactionCommitted
	}

	t.changes = append(t.changes, change{
		path:    path,
		content: nil,
	})

	return nil
}

// Commit applies all changes and creates a git commit.
func (t *localTransaction) Commit(message string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.committed {
		return apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	worktree, err := t.store.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Apply all changes tracked in this transaction
	for i := range t.changes {
		change := &t.changes[i]
		if applyErr := t.applyChange(change, worktree); applyErr != nil {
			return applyErr
		}
	}

	// Stage all changes in the worktree (equivalent to git add -A)
	if addErr := worktree.AddWithOptions(&git.AddOptions{All: true}); addErr != nil {
		return fmt.Errorf("git add: %w", addErr)
	}

	// Check if there are any staged changes
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("get status: %w", err)
	}

	hasChanges := false
	for _, s := range status {
		if s.Staging != ' ' {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		t.committed = true
		return nil
	}

	// Determine author from remote config or use defaults
	authorName := "notion-git-sync"
	authorEmail := "notion-git-sync@localhost"
	if t.store.remoteConfig != nil {
		authorName = t.store.remoteConfig.User
		authorEmail = t.store.remoteConfig.Email
	}

	// Create commit
	_, err = worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	t.committed = true
	return nil
}

// Rollback discards all pending changes.
func (t *localTransaction) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.changes = nil
	t.committed = true
	return nil
}

// applyChange applies a single change (write or delete) to the filesystem and git worktree.
func (t *localTransaction) applyChange(change *change, worktree *git.Worktree) error {
	fullPath := filepath.Join(t.store.rootPath, change.path)

	if change.content == nil {
		// Delete
		if rmErr := os.Remove(fullPath); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("delete %s: %w", change.path, rmErr)
		}
		// Try to remove from git, ignore errors if file wasn't tracked
		_, _ = worktree.Remove(change.path)
		return nil
	}

	// Write
	if mkErr := os.MkdirAll(filepath.Dir(fullPath), dirPerm); mkErr != nil {
		return fmt.Errorf("mkdir for %s: %w", change.path, mkErr)
	}

	if wrErr := os.WriteFile(fullPath, change.content, filePerm); wrErr != nil {
		return fmt.Errorf("write %s: %w", change.path, wrErr)
	}

	if _, addErr := worktree.Add(change.path); addErr != nil {
		return fmt.Errorf("git add %s: %w", change.path, addErr)
	}

	return nil
}

// initializeRepository initializes a git repository, either by cloning from remote or creating locally.
func (s *LocalStore) initializeRepository(path string) (*git.Repository, error) {
	_, statErr := os.Stat(path)
	dirExists := statErr == nil

	// Try to clone from remote if enabled and directory doesn't exist
	if s.remoteConfig.IsEnabled() && !dirExists {
		return s.cloneFromRemote(path)
	}

	// Otherwise open or create local repository
	return s.openOrCreateLocalRepo(path)
}

// cloneFromRemote clones a repository from the remote URL.
func (s *LocalStore) cloneFromRemote(path string) (*git.Repository, error) {
	s.logger.Info("cloning from remote", "url", s.remoteConfig.URL, "branch", s.remoteConfig.Branch)

	auth, err := s.remoteConfig.GetAuth()
	if err != nil {
		return nil, fmt.Errorf("get auth: %w", err)
	}

	repo, err := git.PlainClone(path, false, &git.CloneOptions{
		URL:           s.remoteConfig.URL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(s.remoteConfig.Branch),
		SingleBranch:  true,
	})

	if err == nil {
		s.logger.Info("clone complete")
		return repo, nil
	}

	// Handle empty repository - init locally and add remote
	if err.Error() != msgRemoteRepoEmpty {
		return nil, fmt.Errorf("clone repository: %w", err)
	}

	return s.initRepoWithRemote(path)
}

// initRepoWithRemote initializes a new repository and adds the remote.
func (s *LocalStore) initRepoWithRemote(path string) (*git.Repository, error) {
	s.logger.Info(msgRemoteRepoEmpty + ", initializing locally")

	if err := os.MkdirAll(path, dirPerm); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	repo, err := git.PlainInit(path, false)
	if err != nil {
		return nil, fmt.Errorf("init git repo: %w", err)
	}

	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{s.remoteConfig.URL},
	})
	if err != nil {
		return nil, fmt.Errorf("add remote origin: %w", err)
	}

	return repo, nil
}

// openOrCreateLocalRepo opens an existing repository or creates a new one.
func (s *LocalStore) openOrCreateLocalRepo(path string) (*git.Repository, error) {
	// Ensure path exists
	if err := os.MkdirAll(path, dirPerm); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	// Try to open existing repository
	repo, err := git.PlainOpen(path)
	if err == nil {
		return s.ensureRemoteConfigured(repo)
	}

	// Repository doesn't exist - create it
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("open git repo: %w", err)
	}

	return s.initNewRepo(path)
}

// initNewRepo initializes a new git repository and optionally adds remote.
func (s *LocalStore) initNewRepo(path string) (*git.Repository, error) {
	repo, err := git.PlainInit(path, false)
	if err != nil {
		return nil, fmt.Errorf("init git repo: %w", err)
	}

	// If remote is configured, add it to the new repo
	if s.remoteConfig.IsEnabled() {
		if err := s.addRemoteToRepo(repo); err != nil {
			return nil, err
		}
	}

	return repo, nil
}

// ensureRemoteConfigured ensures the remote is configured in an existing repository.
func (s *LocalStore) ensureRemoteConfigured(repo *git.Repository) (*git.Repository, error) {
	if !s.remoteConfig.IsEnabled() {
		return repo, nil
	}

	// Check if remote exists
	if _, err := repo.Remote("origin"); err == nil {
		return repo, nil
	}

	// Remote doesn't exist, add it
	s.logger.Info("adding remote origin to existing repo", "url", s.remoteConfig.URL)
	if err := s.addRemoteToRepo(repo); err != nil {
		return nil, err
	}

	return repo, nil
}

// addRemoteToRepo adds the origin remote to a repository.
func (s *LocalStore) addRemoteToRepo(repo *git.Repository) error {
	_, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{s.remoteConfig.URL},
	})
	if err != nil {
		return fmt.Errorf("add remote origin: %w", err)
	}
	return nil
}
