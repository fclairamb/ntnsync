package store

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	// gitHubTreeCacheSize bounds the directory-listing cache. Entries are keyed
	// by tree SHA, so this is purely a memory bound.
	gitHubTreeCacheSize = 256

	// gitHubBlobCacheSize bounds the cached shard bodies.
	gitHubBlobCacheSize = 128

	// gitHubCacheablePrefix is the only path prefix whose blob bodies are
	// cached. Registry shards are small, hot and read repeatedly within a
	// cycle; caching page content would reintroduce the memory problem this
	// backend exists to avoid.
	gitHubCacheablePrefix = ".notion-sync/ids/"
)

// GitHubStore implements Store on top of the GitHub Git Data API.
//
// It holds no working tree and writes nothing to the local filesystem: reads
// descend the tree one directory at a time and writes are buffered until the
// transaction commits, which becomes a blob/tree/commit/ref sequence.
type GitHubStore struct {
	api    *githubAPI
	cfg    *RemoteConfig
	branch string
	owner  string
	repo   string
	logger *slog.Logger

	// mu backs Lock/Unlock for external coordination, mirroring LocalStore.
	mu sync.RWMutex

	// stateMu guards the cached HEAD of the branch.
	stateMu     sync.Mutex
	headSHA     string
	headTreeSHA string
	headKnown   bool

	treeCache *lruCache[[]gitHubTreeEntry]
	blobCache *lruCache[[]byte]

	// txMu guards the list of transactions whose buffered writes reads must see.
	txMu    sync.Mutex
	openTxs []*githubTransaction
}

// GitHubStoreOption configures a GitHubStore.
type GitHubStoreOption func(*gitHubStoreOptions)

// gitHubStoreOptions collects optional GitHubStore settings.
type gitHubStoreOptions struct {
	branch     string
	logger     *slog.Logger
	apiOptions []githubAPIOption
}

// WithGitHubBranch overrides the branch taken from the remote configuration.
// The queue store uses it to target NTN_QUEUE_BRANCH.
func WithGitHubBranch(branch string) GitHubStoreOption {
	return func(o *gitHubStoreOptions) {
		if branch != "" {
			o.branch = branch
		}
	}
}

// WithGitHubStoreLogger sets the logger for the store and its API client.
func WithGitHubStoreLogger(logger *slog.Logger) GitHubStoreOption {
	return func(o *gitHubStoreOptions) {
		o.logger = logger
	}
}

// withGitHubAPIOptions injects API client options; used by tests to supply a
// stub http.RoundTripper and a fake clock.
func withGitHubAPIOptions(opts ...githubAPIOption) GitHubStoreOption {
	return func(o *gitHubStoreOptions) {
		o.apiOptions = append(o.apiOptions, opts...)
	}
}

// NewGitHubStore validates the configuration and builds a GitHub API store.
// It fails immediately when NTN_GIT_URL is not a github.com repository URL or
// NTN_GIT_PASS is missing.
func NewGitHubStore(cfg *RemoteConfig, opts ...GitHubStoreOption) (*GitHubStore, error) {
	owner, repo, err := cfg.GitHubRepo()
	if err != nil {
		return nil, err
	}

	settings := &gitHubStoreOptions{branch: cfg.Branch, logger: slog.Default()}
	for _, opt := range opts {
		opt(settings)
	}

	if settings.branch == "" {
		settings.branch = defaultBranch
	}

	apiOpts := append([]githubAPIOption{withGitHubLogger(settings.logger)}, settings.apiOptions...)

	return &GitHubStore{
		api:       newGitHubAPI(owner, repo, cfg.Password, apiOpts...),
		cfg:       cfg,
		branch:    settings.branch,
		owner:     owner,
		repo:      repo,
		logger:    settings.logger,
		treeCache: newLRUCache[[]gitHubTreeEntry](gitHubTreeCacheSize),
		blobCache: newLRUCache[[]byte](gitHubBlobCacheSize),
	}, nil
}

// Branch returns the branch this store reads and writes.
func (s *GitHubStore) Branch() string {
	return s.branch
}

// normalizeStorePath turns a store-relative path into a clean, slash-separated
// repository path. The repository root is the empty string.
func normalizeStorePath(raw string) string {
	cleaned := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")

	segments := make([]string, 0, strings.Count(cleaned, "/")+1)
	for segment := range strings.SplitSeq(cleaned, "/") {
		if segment == "" || segment == "." {
			continue
		}
		segments = append(segments, segment)
	}

	return strings.Join(segments, "/")
}

// Read reads a file, preferring any pending write from an open transaction.
func (s *GitHubStore) Read(ctx context.Context, path string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clean := normalizeStorePath(path)

	if change, found := s.pendingChange(clean); found {
		if change.deleted {
			return nil, fmt.Errorf("read file %s: %w", path, fs.ErrNotExist)
		}
		return slices.Clone(change.content), nil
	}

	entry, found, err := s.resolveEntry(ctx, clean)
	if err != nil {
		return nil, err
	}
	if !found || entry.Type != gitHubTypeBlob {
		return nil, fmt.Errorf("read file %s: %w", path, fs.ErrNotExist)
	}

	return s.readBlob(ctx, clean, entry.SHA)
}

// readBlob fetches a blob, using the bounded shard cache where applicable.
func (s *GitHubStore) readBlob(ctx context.Context, clean, blobSHA string) ([]byte, error) {
	cacheable := strings.HasPrefix(clean, gitHubCacheablePrefix)

	if cacheable {
		if cached, ok := s.blobCache.Get(blobSHA); ok {
			return slices.Clone(cached), nil
		}
	}

	data, err := s.api.getBlob(ctx, blobSHA)
	if err != nil {
		return nil, err
	}

	if cacheable {
		s.blobCache.Put(blobSHA, slices.Clone(data))
	}

	return data, nil
}

// Exists reports whether a path resolves to an object, pending writes included.
func (s *GitHubStore) Exists(ctx context.Context, path string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clean := normalizeStorePath(path)

	if change, found := s.pendingChange(clean); found {
		return !change.deleted, nil
	}

	_, found, err := s.resolveEntry(ctx, clean)
	if err != nil {
		return false, err
	}

	return found, nil
}

// List lists one directory with a single non-recursive tree fetch, merged with
// the pending writes and deletions of any open transaction.
func (s *GitHubStore) List(ctx context.Context, dir string) ([]FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clean := normalizeStorePath(dir)

	entries, err := s.listTreeEntries(ctx, clean)
	if err != nil {
		return nil, err
	}

	files := make(map[string]FileInfo, len(entries))
	for idx := range entries {
		fullPath := joinStorePath(clean, entries[idx].Path)
		files[fullPath] = FileInfo{
			Path:  fullPath,
			IsDir: entries[idx].Type == gitHubTypeTree,
			Size:  entries[idx].Size,
		}
	}

	s.applyPendingToListing(clean, files)

	out := make([]FileInfo, 0, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		out = append(out, files[name])
	}

	if len(out) == 0 {
		return nil, nil
	}

	return out, nil
}

// listTreeEntries returns the raw tree entries of a directory, or nil when the
// directory does not exist (mirroring LocalStore, which reports no error).
func (s *GitHubStore) listTreeEntries(ctx context.Context, clean string) ([]gitHubTreeEntry, error) {
	if clean == "" {
		rootTree, err := s.rootTreeSHA(ctx)
		if err != nil {
			return nil, err
		}
		if rootTree == "" {
			return nil, nil
		}
		return s.treeEntries(ctx, rootTree)
	}

	entry, found, err := s.resolveEntry(ctx, clean)
	if err != nil {
		return nil, err
	}
	if !found || entry.Type != gitHubTypeTree {
		return nil, nil
	}

	return s.treeEntries(ctx, entry.SHA)
}

// applyPendingToListing overlays an open transaction's buffered changes onto a
// directory listing so a read-back within a cycle sees them.
func (s *GitHubStore) applyPendingToListing(clean string, files map[string]FileInfo) {
	prefix := ""
	if clean != "" {
		prefix = clean + "/"
	}

	pending := s.pendingChanges()
	for _, path := range slices.Sorted(maps.Keys(pending)) {
		if !strings.HasPrefix(path, prefix) {
			continue
		}

		remainder := strings.TrimPrefix(path, prefix)
		if remainder == "" {
			continue
		}

		deleted := pending[path].deleted

		if child, _, nested := strings.Cut(remainder, "/"); nested {
			// The change is nested deeper; it implies the intermediate dir.
			if !deleted {
				dirPath := joinStorePath(clean, child)
				files[dirPath] = FileInfo{Path: dirPath, IsDir: true}
			}
			continue
		}

		if deleted {
			delete(files, path)
			continue
		}

		files[path] = FileInfo{Path: path, Size: int64(len(pending[path].content))}
	}
}

// joinStorePath joins a directory and a name into a repository path.
func joinStorePath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// resolveEntry walks the tree one directory at a time until it reaches path.
//
// It never issues recursive=1: at the repository sizes this backend targets the
// recursive form is truncated, which would make reads silently wrong.
func (s *GitHubStore) resolveEntry(ctx context.Context, clean string) (gitHubTreeEntry, bool, error) {
	rootTree, err := s.rootTreeSHA(ctx)
	if err != nil {
		return gitHubTreeEntry{}, false, err
	}
	if rootTree == "" {
		return gitHubTreeEntry{}, false, nil
	}

	if clean == "" {
		return gitHubTreeEntry{Path: "", Mode: gitHubTreeMode, Type: gitHubTypeTree, SHA: rootTree}, true, nil
	}

	segments := strings.Split(clean, "/")
	current := rootTree

	for idx, segment := range segments {
		entries, listErr := s.treeEntries(ctx, current)
		if listErr != nil {
			return gitHubTreeEntry{}, false, listErr
		}

		match, found := findTreeEntry(entries, segment)
		if !found {
			return gitHubTreeEntry{}, false, nil
		}

		if idx == len(segments)-1 {
			return match, true, nil
		}

		if match.Type != gitHubTypeTree {
			return gitHubTreeEntry{}, false, nil
		}

		current = match.SHA
	}

	return gitHubTreeEntry{}, false, nil
}

// findTreeEntry looks a name up in a tree listing.
func findTreeEntry(entries []gitHubTreeEntry, name string) (gitHubTreeEntry, bool) {
	for idx := range entries {
		if entries[idx].Path == name {
			return entries[idx], true
		}
	}
	return gitHubTreeEntry{}, false
}

// treeEntries returns the entries of one tree, from cache when possible.
//
// Every SHA reaching this function came from a commit object (via
// getCommitTree) or from an entry of a parent tree that was just listed, and
// both callers short-circuit before this point when the branch has no commits
// at all. A missing tree is therefore an API anomaly, never a normal state, and
// the error is propagated rather than reported as "empty directory": swallowing
// it would make Read report not-found, Exists report false, and — worst — the
// commit-time deletion filter conclude a path is absent and silently drop a
// legitimate deletion from the backup.
func (s *GitHubStore) treeEntries(ctx context.Context, treeSHA string) ([]gitHubTreeEntry, error) {
	if cached, ok := s.treeCache.Get(treeSHA); ok {
		return cached, nil
	}

	entries, err := s.api.getTree(ctx, treeSHA)
	if err != nil {
		return nil, fmt.Errorf("list tree %s: %w", treeSHA, err)
	}

	s.treeCache.Put(treeSHA, entries)

	return entries, nil
}

// ensureHead resolves and caches the branch HEAD and its root tree.
func (s *GitHubStore) ensureHead(ctx context.Context) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.headKnown {
		return nil
	}

	commitSHA, found, err := s.api.getRef(ctx, s.branch)
	if err != nil {
		return err
	}

	if !found {
		s.headSHA = ""
		s.headTreeSHA = ""
		s.headKnown = true
		s.logger.InfoContext(ctx, "github branch does not exist yet, first commit will create it",
			"branch", s.branch, "repo", s.owner+"/"+s.repo)
		return nil
	}

	treeSHA, err := s.api.getCommitTree(ctx, commitSHA)
	if err != nil {
		return err
	}

	s.headSHA = commitSHA
	s.headTreeSHA = treeSHA
	s.headKnown = true

	return nil
}

// rootTreeSHA returns the cached root tree SHA, resolving HEAD if needed.
// An empty result means the branch has no commits yet.
func (s *GitHubStore) rootTreeSHA(ctx context.Context) (string, error) {
	if err := s.ensureHead(ctx); err != nil {
		return "", err
	}

	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	return s.headTreeSHA, nil
}

// headSnapshot returns the cached commit and root tree SHAs.
func (s *GitHubStore) headSnapshot() (commitSHA, treeSHA string) { //nolint:nonamedreturns // named for documentation
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	return s.headSHA, s.headTreeSHA
}

// setHead records a newly created commit as the branch HEAD.
func (s *GitHubStore) setHead(commitSHA, treeSHA string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	s.headSHA = commitSHA
	s.headTreeSHA = treeSHA
	s.headKnown = true
}

// invalidateHead forces the next read to re-resolve the branch HEAD. The tree
// and blob caches stay valid because they are keyed by content-addressed SHAs.
func (s *GitHubStore) invalidateHead() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	s.headKnown = false
}

// BeginTx starts a buffered transaction.
func (s *GitHubStore) BeginTx(_ context.Context) (Transaction, error) {
	return &githubTransaction{
		store:   s,
		changes: make(map[string]*githubChange),
	}, nil
}

// Push is a no-op: a committed transaction is already on the remote.
func (s *GitHubStore) Push(ctx context.Context) error {
	s.logger.DebugContext(ctx, "github storage commits directly to the remote, nothing to push")
	return nil
}

// Pull refreshes the cached branch HEAD and reports the rate-limit budget.
func (s *GitHubStore) Pull(ctx context.Context) error {
	s.invalidateHead()

	if err := s.ensureHead(ctx); err != nil {
		return err
	}

	commitSHA, _ := s.headSnapshot()
	s.logger.InfoContext(ctx, "github head refreshed",
		"repo", s.owner+"/"+s.repo, "branch", s.branch, "head", commitSHA)
	s.api.logRateBudget(ctx)

	return nil
}

// Lock acquires the store's write lock for external coordination.
func (s *GitHubStore) Lock() {
	s.mu.Lock()
}

// Unlock releases the store's write lock.
func (s *GitHubStore) Unlock() {
	s.mu.Unlock()
}

// IsRemoteEnabled always returns true: the GitHub backend has no local mode.
func (s *GitHubStore) IsRemoteEnabled() bool {
	return true
}

// RemoteConfig returns the remote configuration backing this store.
func (s *GitHubStore) RemoteConfig() *RemoteConfig {
	return s.cfg
}

// CheckWholeTreeSupported rejects commands that need to walk the whole
// repository. Those need the truncation-prone recursive listing this backend
// deliberately avoids, so they fail fast instead of half-working.
func (s *GitHubStore) CheckWholeTreeSupported(command string) error {
	return fmt.Errorf("%s: %w", command, apperrors.ErrGitHubWholeTreeUnsupported)
}

// TestConnection verifies credentials and repository access.
func (s *GitHubStore) TestConnection(ctx context.Context) error {
	s.invalidateHead()

	if err := s.ensureHead(ctx); err != nil {
		return err
	}

	s.api.logRateBudget(ctx)

	return nil
}

// trackTx prunes transactions with nothing buffered and registers tx so its
// pending writes become visible to reads.
func (s *GitHubStore) trackTx(target *githubTransaction) {
	s.txMu.Lock()
	defer s.txMu.Unlock()

	live := s.openTxs[:0]
	for _, open := range s.openTxs {
		if open == target || open.hasPending() {
			live = append(live, open)
		}
	}
	s.openTxs = live

	if !slices.Contains(s.openTxs, target) {
		s.openTxs = append(s.openTxs, target)
	}
}

// untrackTx removes a transaction from the pending-read overlay.
func (s *GitHubStore) untrackTx(target *githubTransaction) {
	s.txMu.Lock()
	defer s.txMu.Unlock()

	s.openTxs = slices.DeleteFunc(s.openTxs, func(open *githubTransaction) bool {
		return open == target
	})
}

// pendingChange returns the buffered change for a path, if any. The most
// recently registered transaction wins.
func (s *GitHubStore) pendingChange(clean string) (githubChange, bool) {
	s.txMu.Lock()
	transactions := slices.Clone(s.openTxs)
	s.txMu.Unlock()

	for _, open := range slices.Backward(transactions) {
		if change, found := open.change(clean); found {
			return change, true
		}
	}

	return githubChange{}, false
}

// pendingChanges returns every buffered change across open transactions,
// oldest transaction first so newer ones override.
func (s *GitHubStore) pendingChanges() map[string]githubChange {
	s.txMu.Lock()
	transactions := slices.Clone(s.openTxs)
	s.txMu.Unlock()

	merged := make(map[string]githubChange)
	for _, open := range transactions {
		maps.Copy(merged, open.snapshot())
	}

	return merged
}
