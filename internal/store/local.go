package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	msgRemoteRepoEmpty = "remote repository is empty"

	// File and directory permissions.
	dirPerm  = 0750 // Directory permissions: rwxr-x---
	filePerm = 0600 // File permissions: rw-------

	// maxTargetedStagingPaths is the number of modified paths above which
	// staging each path individually stops paying off. Every targeted add
	// decodes and re-encodes the whole index, so past this many paths the
	// single whole-worktree merkletrie walk is cheaper.
	maxTargetedStagingPaths = 500
)

// LocalStore implements Store using local filesystem and git.
type LocalStore struct {
	rootPath              string
	repo                  *git.Repository
	mu                    sync.RWMutex
	logger                *slog.Logger
	remoteConfig          *RemoteConfig
	createBranchIfMissing bool
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

// WithCreateBranchIfMissing makes the store initialize a fresh local branch
// (pushed lazily on first commit) when the configured branch does not yet
// exist on the remote, instead of failing to clone. Used for the queue branch.
func WithCreateBranchIfMissing() LocalStoreOption {
	return func(s *LocalStore) {
		s.createBranchIfMissing = true
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

// BeginTx starts a new transaction.
func (s *LocalStore) BeginTx(_ context.Context) (Transaction, error) {
	return &localTransaction{
		store:         s,
		modifiedPaths: make(map[string]bool),
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

	return s.pullLocked(ctx)
}

// pullLocked performs the actual pull operation. Caller must hold s.mu.
// It handles diverged branches by creating a merge commit when necessary.
func (s *LocalStore) pullLocked(ctx context.Context) error {
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
		RemoteName:    gitRemoteOrigin,
		ReferenceName: plumbing.NewBranchReferenceName(s.remoteConfig.Branch),
		Auth:          auth,
		Depth:         s.remoteConfig.Depth,
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
		// Handle non-fast-forward case (diverged branches)
		if strings.Contains(err.Error(), "non-fast-forward") {
			s.logger.InfoContext(ctx, "branches diverged, fetching and merging")
			return s.fetchAndMergeLocked(ctx, auth, worktree)
		}
		return fmt.Errorf("pull: %w", err)
	}

	s.logger.InfoContext(ctx, "pull complete")
	return nil
}

// fetchAndMergeLocked fetches remote changes and resets to remote.
// For auto-generated content like ntnsync, we favor the remote version
// since it's already published. The sync process will re-apply any changes.
func (s *LocalStore) fetchAndMergeLocked(ctx context.Context, auth transport.AuthMethod, worktree *git.Worktree) error {
	// Fetch remote changes
	err := s.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: gitRemoteOrigin,
		Auth:       auth,
		Depth:      s.remoteConfig.Depth,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch: %w", err)
	}

	// Get remote branch reference
	remoteBranch := plumbing.NewRemoteReferenceName(gitRemoteOrigin, s.remoteConfig.Branch)
	remoteRef, err := s.repo.Reference(remoteBranch, true)
	if err != nil {
		return fmt.Errorf("get remote ref: %w", err)
	}

	s.logger.InfoContext(ctx, "resetting to remote",
		"remote_commit", remoteRef.Hash().String()[:7])

	// Reset to remote - this is safe for auto-generated content
	if err := worktree.Reset(&git.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   git.HardReset,
	}); err != nil {
		return fmt.Errorf("reset to remote: %w", err)
	}

	// Update the local branch reference to point to the remote commit
	branchRef := plumbing.NewBranchReferenceName(s.remoteConfig.Branch)
	ref := plumbing.NewHashReference(branchRef, remoteRef.Hash())
	if err := s.repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("update branch ref: %w", err)
	}

	s.logger.InfoContext(ctx, "reset to remote complete")
	return nil
}

// Push pushes local commits to the remote repository.
// If a non-fast-forward error occurs, it will attempt to pull first and retry the push.
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

	err = s.pushLocked(ctx, auth)
	if err == nil {
		return nil
	}

	if !strings.Contains(err.Error(), "non-fast-forward") {
		return err
	}

	// Handle non-fast-forward error by pulling and retrying
	s.logger.WarnContext(ctx, "push rejected (non-fast-forward), pulling and retrying", "error", err)

	if pullErr := s.pullLocked(ctx); pullErr != nil {
		return fmt.Errorf("pull before retry: %w", pullErr)
	}

	s.logger.InfoContext(ctx, "retrying push after pull")

	return s.pushLocked(ctx, auth)
}

// pushLocked performs the actual push operation. Caller must hold s.mu.
func (s *LocalStore) pushLocked(ctx context.Context, auth transport.AuthMethod) error {
	s.logger.InfoContext(ctx, "pushing to remote", "url", s.remoteConfig.URL, "branch", s.remoteConfig.Branch)

	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", s.remoteConfig.Branch, s.remoteConfig.Branch))
	err := s.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: gitRemoteOrigin,
		Auth:       auth,
		RefSpecs:   []config.RefSpec{refSpec},
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
		Name: gitRemoteOrigin,
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
// All write operations are applied immediately to the filesystem.
// Commit stages all changes and creates a git commit.
type localTransaction struct {
	store         *LocalStore
	modifiedPaths map[string]bool // tracks paths modified since last commit
	mu            sync.Mutex
	closed        bool
}

// Write writes content to a file immediately.
func (t *localTransaction) Write(_ context.Context, path string, content []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	fullPath := filepath.Join(t.store.rootPath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, filePerm); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}

	t.modifiedPaths[path] = true
	return nil
}

// WriteStream writes content from a reader to a file using streaming.
// This avoids loading the entire content into memory.
// Returns the number of bytes written.
func (t *localTransaction) WriteStream(_ context.Context, path string, reader io.Reader) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return 0, apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	fullPath := filepath.Join(t.store.rootPath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
		return 0, fmt.Errorf("create parent dir: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tmpFile, err := os.CreateTemp(filepath.Dir(fullPath), ".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup on failure
	success := false
	defer func() {
		_ = tmpFile.Close()
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	written, err := io.Copy(tmpFile, reader)
	if err != nil {
		return written, fmt.Errorf("write content: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return written, fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, filePerm); err != nil {
		return written, fmt.Errorf("set permissions: %w", err)
	}

	if err := os.Rename(tmpPath, fullPath); err != nil {
		return written, fmt.Errorf("rename temp file: %w", err)
	}

	success = true
	t.modifiedPaths[path] = true
	return written, nil
}

// Delete deletes a file immediately.
func (t *localTransaction) Delete(_ context.Context, path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	fullPath := filepath.Join(t.store.rootPath, path)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file %s: %w", path, err)
	}

	t.modifiedPaths[path] = true
	return nil
}

// Mkdir creates a directory.
func (t *localTransaction) Mkdir(_ context.Context, path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	fullPath := filepath.Join(t.store.rootPath, path)
	if err := os.MkdirAll(fullPath, dirPerm); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}

	return nil
}

// stageTargeted stages exactly the paths recorded in modifiedPaths, avoiding the
// two whole-worktree walks performed by AddWithOptions{All: true} + Status().
//
// A path that still exists on disk is staged with SkipStatus so go-git does not
// fall back to a full Status() walk. A path that no longer exists was deleted by
// Delete, so it is removed from the index instead; a path written and then
// deleted within the same transaction was never in the index, which is not an
// error here.
func (t *localTransaction) stageTargeted(worktree *git.Worktree) error {
	for path := range t.modifiedPaths {
		gitPath := filepath.ToSlash(path)
		fullPath := filepath.Join(t.store.rootPath, filepath.FromSlash(path))

		_, statErr := os.Lstat(fullPath)
		if statErr == nil {
			if err := worktree.AddWithOptions(&git.AddOptions{Path: gitPath, SkipStatus: true}); err != nil {
				return fmt.Errorf("git add %s: %w", path, err)
			}
			continue
		}

		if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat %s: %w", path, statErr)
		}

		if _, err := worktree.Remove(gitPath); err != nil {
			// The path was never tracked (written and deleted in the same
			// transaction, or already absent from the index): nothing to stage.
			if errors.Is(err, index.ErrEntryNotFound) {
				continue
			}
			return fmt.Errorf("git rm %s: %w", path, err)
		}
	}

	return nil
}

// headTree returns the tree of the current HEAD commit, or nil when the
// repository has no commit yet.
func (t *localTransaction) headTree() (*object.Tree, error) {
	head, err := t.store.repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil //nolint:nilnil // no HEAD yet is a valid, non-error state
		}
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}

	commit, err := t.store.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("read HEAD commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read HEAD tree: %w", err)
	}

	return tree, nil
}

// stagedPathsDiffer reports whether the index differs from HEAD for any of the
// paths recorded in modifiedPaths. A path may be listed there while its content
// is identical to HEAD (a rewrite with the same bytes); staging it is a no-op
// for the resulting tree and must not be counted as a change, otherwise empty
// commits would be created.
func (t *localTransaction) stagedPathsDiffer() (bool, error) {
	idx, err := t.store.repo.Storer.Index()
	if err != nil {
		return false, fmt.Errorf("read index: %w", err)
	}

	tree, err := t.headTree()
	if err != nil {
		return false, err
	}

	for path := range t.modifiedPaths {
		gitPath := filepath.ToSlash(path)

		indexHash, inIndex, err := indexEntryHash(idx, gitPath)
		if err != nil {
			return false, err
		}

		headHash, inHead, err := treeEntryHash(tree, gitPath)
		if err != nil {
			return false, err
		}

		if inIndex != inHead || indexHash != headHash {
			return true, nil
		}
	}

	return false, nil
}

// indexEntryHash returns the blob hash staged for gitPath and whether the path
// is present in the index at all.
func indexEntryHash(idx *index.Index, gitPath string) (plumbing.Hash, bool, error) {
	entry, err := idx.Entry(gitPath)
	if err != nil {
		if errors.Is(err, index.ErrEntryNotFound) {
			return plumbing.ZeroHash, false, nil
		}
		return plumbing.ZeroHash, false, fmt.Errorf("index entry %s: %w", gitPath, err)
	}

	return entry.Hash, true, nil
}

// treeEntryHash returns the blob hash recorded for gitPath in tree and whether
// the path is present in it. A nil tree (no HEAD yet) has no entries.
func treeEntryHash(tree *object.Tree, gitPath string) (plumbing.Hash, bool, error) {
	if tree == nil {
		return plumbing.ZeroHash, false, nil
	}

	entry, err := tree.FindEntry(gitPath)
	if err != nil {
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
			return plumbing.ZeroHash, false, nil
		}
		return plumbing.ZeroHash, false, fmt.Errorf("HEAD entry %s: %w", gitPath, err)
	}

	return entry.Hash, true, nil
}

// stageWholeWorktree is the fallback used above maxTargetedStagingPaths, where
// the per-path index rewrites cost more than a single merkletrie walk.
func stageWholeWorktree(worktree *git.Worktree) (bool, error) {
	// Stage all changes in the worktree (equivalent to git add -A).
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}

	status, err := worktree.Status()
	if err != nil {
		return false, fmt.Errorf("get status: %w", err)
	}

	for _, fileStatus := range status {
		if fileStatus.Staging != ' ' {
			return true, nil
		}
	}

	return false, nil
}

// stageChanges stages the transaction's pending changes and reports whether the
// resulting index differs from HEAD.
func (t *localTransaction) stageChanges(worktree *git.Worktree) (bool, error) {
	if len(t.modifiedPaths) > maxTargetedStagingPaths {
		return stageWholeWorktree(worktree)
	}

	if err := t.stageTargeted(worktree); err != nil {
		return false, err
	}

	return t.stagedPathsDiffer()
}

// Commit stages all changes and creates a git commit.
// After commit, the transaction can continue to be used for more changes.
func (t *localTransaction) Commit(_ context.Context, message string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	worktree, err := t.store.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	hasChanges, err := t.stageChanges(worktree)
	if err != nil {
		return err
	}

	if !hasChanges {
		// Clear modified paths since there's nothing to commit
		t.modifiedPaths = make(map[string]bool)
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

	// Clear modified paths after successful commit
	t.modifiedPaths = make(map[string]bool)
	return nil
}

// Rollback discards all uncommitted changes and closes the transaction.
func (t *localTransaction) Rollback(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	// Reset the working directory to HEAD
	worktree, err := t.store.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Only reset if there are changes
	if len(t.modifiedPaths) > 0 {
		if err := worktree.Reset(&git.ResetOptions{Mode: git.HardReset}); err != nil {
			return fmt.Errorf("reset worktree: %w", err)
		}
	}

	t.modifiedPaths = nil
	t.closed = true
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
		Depth:         s.remoteConfig.Depth,
	})

	if err == nil {
		s.logger.Info("clone complete")
		return repo, nil
	}

	// Empty remote repository - init locally and add remote.
	if err.Error() == msgRemoteRepoEmpty {
		return s.initRepoWithRemote(path)
	}

	// Configured branch does not exist yet on a non-empty remote. For stores
	// allowed to create their branch (e.g. the queue branch), initialize a
	// fresh local branch that gets pushed on the first commit.
	if s.createBranchIfMissing && isBranchNotFoundErr(err) {
		s.logger.Info("remote branch not found, will create it on first push",
			"branch", s.remoteConfig.Branch)
		return s.initRepoWithRemote(path)
	}

	return nil, fmt.Errorf("clone repository: %w", err)
}

// isBranchNotFoundErr reports whether a clone error indicates the requested
// branch reference does not exist on the remote.
func isBranchNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "reference not found") ||
		strings.Contains(msg, "couldn't find remote ref") ||
		strings.Contains(msg, "remote ref not found")
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

	// Set the default branch to the configured branch
	if err = s.setDefaultBranch(repo); err != nil {
		return nil, err
	}

	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: gitRemoteOrigin,
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

	// Set the default branch to the configured branch
	if err := s.setDefaultBranch(repo); err != nil {
		return nil, err
	}

	// If remote is configured, add it to the new repo
	if s.remoteConfig.IsEnabled() {
		if err := s.addRemoteToRepo(repo); err != nil {
			return nil, err
		}
	}

	return repo, nil
}

// setDefaultBranch sets HEAD to point to the configured branch.
// This ensures the repo uses the correct branch name (e.g., "main" instead of "master").
func (s *LocalStore) setDefaultBranch(repo *git.Repository) error {
	branch := defaultBranch
	if s.remoteConfig != nil && s.remoteConfig.Branch != "" {
		branch = s.remoteConfig.Branch
	}
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))
	if err := repo.Storer.SetReference(headRef); err != nil {
		return fmt.Errorf("set default branch to %s: %w", branch, err)
	}
	return nil
}

// ensureRemoteConfigured ensures the remote is configured in an existing repository.
func (s *LocalStore) ensureRemoteConfigured(repo *git.Repository) (*git.Repository, error) {
	if !s.remoteConfig.IsEnabled() {
		return repo, nil
	}

	// Check if remote exists
	remote, err := repo.Remote(gitRemoteOrigin)
	if err != nil {
		// Remote doesn't exist, add it
		s.logger.Info("adding remote origin to existing repo", "url", s.remoteConfig.URL)
		if err := s.addRemoteToRepo(repo); err != nil {
			return nil, err
		}
		return repo, nil
	}

	// Remote exists, check if URL matches
	cfg := remote.Config()
	if len(cfg.URLs) > 0 && cfg.URLs[0] == s.remoteConfig.URL {
		return repo, nil
	}

	// URL mismatch, update the remote
	s.logger.Info("updating remote origin URL", "old", cfg.URLs, "new", s.remoteConfig.URL)
	if err := repo.DeleteRemote(gitRemoteOrigin); err != nil {
		return nil, fmt.Errorf("delete old remote origin: %w", err)
	}
	if err := s.addRemoteToRepo(repo); err != nil {
		return nil, err
	}

	return repo, nil
}

// addRemoteToRepo adds the origin remote to a repository.
func (s *LocalStore) addRemoteToRepo(repo *git.Repository) error {
	_, err := repo.CreateRemote(&config.RemoteConfig{
		Name: gitRemoteOrigin,
		URLs: []string{s.remoteConfig.URL},
	})
	if err != nil {
		return fmt.Errorf("add remote origin: %w", err)
	}
	return nil
}
