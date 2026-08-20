package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	// gitHubMaxBufferedBytes caps the total base64-encoded payload a single
	// transaction may buffer before committing. Individual files are already
	// bounded by NTN_MAX_FILE_SIZE (512 KB by default); this bounds the batch.
	// The budget is measured in encoded bytes because base64 inflates payloads
	// by 4/3 and it is the encoded form that is held and uploaded.
	gitHubMaxBufferedBytes = 64 << 20

	// gitHubCommitAttempts bounds rebuild-and-retry rounds when the branch ref
	// moves underneath a commit.
	gitHubCommitAttempts = 5

	// gitHubCommitRetryBackoff is the first delay between rebuild attempts.
	gitHubCommitRetryBackoff = 250 * time.Millisecond
)

// githubChange is one buffered write or deletion.
type githubChange struct {
	content []byte
	deleted bool
}

// githubTransaction buffers writes in memory and turns Commit into a
// blob/tree/commit/ref sequence against the GitHub Git Data API.
//
// The API backend has no filesystem to apply writes to immediately, so reads
// on the owning store consult these buffered changes first. Without that, a
// read-modify-write of the same registry shard twice within one commit cycle
// would lose the first write.
type githubTransaction struct {
	store *GitHubStore

	mu       sync.Mutex
	changes  map[string]*githubChange
	buffered int64
	closed   bool

	// pending mirrors len(changes) for lock-free checks from the store.
	pending atomic.Int64
}

// hasPending reports whether the transaction is holding buffered changes.
func (t *githubTransaction) hasPending() bool {
	return t.pending.Load() > 0
}

// change returns the buffered change for a path.
func (t *githubTransaction) change(clean string) (githubChange, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	found, ok := t.changes[clean]
	if !ok {
		return githubChange{}, false
	}

	return *found, true
}

// snapshot copies the buffered changes.
func (t *githubTransaction) snapshot() map[string]githubChange {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[string]githubChange, len(t.changes))
	for path, change := range t.changes {
		out[path] = *change
	}

	return out
}

// Write buffers file content for the next commit.
func (t *githubTransaction) Write(_ context.Context, path string, content []byte) error {
	t.store.trackTx(t)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	clean := normalizeStorePath(path)
	if clean == "" {
		return fmt.Errorf("write %q: %w", path, apperrors.ErrEmptyInput)
	}

	if err := t.reserveLocked(clean, int64(len(content))); err != nil {
		return err
	}

	t.changes[clean] = &githubChange{content: slices.Clone(content)}
	t.pending.Store(int64(len(t.changes)))

	return nil
}

// WriteStream buffers streamed content for the next commit.
func (t *githubTransaction) WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(reader, gitHubMaxBufferedBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read stream for %s: %w", path, err)
	}

	if int64(len(data)) > gitHubMaxBufferedBytes {
		return 0, fmt.Errorf("%w: %s exceeds %d bytes",
			apperrors.ErrGitHubBufferFull, path, int64(gitHubMaxBufferedBytes))
	}

	if err := t.Write(ctx, path, data); err != nil {
		return 0, err
	}

	return int64(len(data)), nil
}

// reserveLocked accounts a write against the transaction byte budget,
// measured in base64-encoded bytes. Caller must hold t.mu.
func (t *githubTransaction) reserveLocked(clean string, size int64) error {
	encoded := int64(base64.StdEncoding.EncodedLen(int(size)))

	replaced := int64(0)
	if existing, ok := t.changes[clean]; ok {
		replaced = int64(base64.StdEncoding.EncodedLen(len(existing.content)))
	}

	next := t.buffered - replaced + encoded
	if next > gitHubMaxBufferedBytes {
		return fmt.Errorf("%w: %d encoded bytes buffered, limit %d",
			apperrors.ErrGitHubBufferFull, next, int64(gitHubMaxBufferedBytes))
	}

	t.buffered = next

	return nil
}

// Delete buffers a deletion for the next commit.
func (t *githubTransaction) Delete(_ context.Context, path string) error {
	t.store.trackTx(t)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return apperrors.ErrTransactionCommitted
	}

	clean := normalizeStorePath(path)
	if clean == "" {
		return fmt.Errorf("delete %q: %w", path, apperrors.ErrEmptyInput)
	}

	if existing, ok := t.changes[clean]; ok {
		t.buffered -= int64(base64.StdEncoding.EncodedLen(len(existing.content)))
	}

	t.changes[clean] = &githubChange{deleted: true}
	t.pending.Store(int64(len(t.changes)))

	return nil
}

// Mkdir is a no-op: git has no directory objects of its own.
func (t *githubTransaction) Mkdir(_ context.Context, _ string) error {
	return nil
}

// Rollback discards every buffered change and closes the transaction.
func (t *githubTransaction) Rollback(_ context.Context) error {
	t.mu.Lock()
	t.changes = make(map[string]*githubChange)
	t.buffered = 0
	t.closed = true
	t.pending.Store(0)
	t.mu.Unlock()

	t.store.untrackTx(t)

	return nil
}

// Commit turns the buffered change set into one GitHub commit.
//
// The sequence is: one blob POST per changed file, one tree POST carrying
// base_tree, one commit POST, one ref PATCH. base_tree is what keeps this
// O(changed files): unchanged paths are inherited, never transferred.
func (t *githubTransaction) Commit(ctx context.Context, message string) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return apperrors.ErrTransactionCommitted
	}
	pending := maps.Clone(t.changes)
	t.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}

	blobs, err := t.uploadBlobs(ctx, pending)
	if err != nil {
		return err
	}

	if err := t.commitWithRetry(ctx, message, pending, blobs); err != nil {
		return err
	}

	t.mu.Lock()
	for path, committed := range pending {
		// Only drop entries that were not overwritten while the commit ran.
		if current, ok := t.changes[path]; ok && current == committed {
			delete(t.changes, path)
		}
	}
	t.buffered = 0
	for _, change := range t.changes {
		t.buffered += int64(base64.StdEncoding.EncodedLen(len(change.content)))
	}
	t.pending.Store(int64(len(t.changes)))
	t.mu.Unlock()

	return nil
}

// uploadBlobs uploads one blob per written path. Blobs are content-addressed
// and stay valid across rebuild attempts, so this happens exactly once.
func (t *githubTransaction) uploadBlobs(
	ctx context.Context, pending map[string]*githubChange,
) (map[string]string, error) {
	blobs := make(map[string]string, len(pending))

	for _, path := range slices.Sorted(maps.Keys(pending)) {
		change := pending[path]
		if change.deleted {
			continue
		}

		sha, err := t.store.api.createBlob(ctx, change.content)
		if err != nil {
			return nil, fmt.Errorf("create blob for %s: %w", path, err)
		}

		blobs[path] = sha
	}

	return blobs, nil
}

// commitWithRetry builds the tree, commit and ref update, rebuilding on a new
// base when the branch ref moved underneath us.
//
// force is never used: it would silently discard the concurrent writer's commit.
func (t *githubTransaction) commitWithRetry(
	ctx context.Context, message string, pending map[string]*githubChange, blobs map[string]string,
) error {
	delay := gitHubCommitRetryBackoff

	var lastErr error

	for attempt := range gitHubCommitAttempts {
		err := t.commitOnce(ctx, message, pending, blobs)
		if err == nil {
			return nil
		}

		if !errors.Is(err, apperrors.ErrGitHubRefMoved) {
			return err
		}

		lastErr = err

		t.store.logger.WarnContext(ctx, "github ref moved, rebuilding commit on the new head",
			"attempt", attempt+1, "max_attempts", gitHubCommitAttempts, "error", err)

		t.store.invalidateHead()

		if attempt == gitHubCommitAttempts-1 {
			break
		}

		if sleepErr := t.store.api.sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}

		delay *= gitHubBackoffFactor
	}

	return fmt.Errorf("%w: %w", apperrors.ErrMaxRetriesExceeded, lastErr)
}

// commitOnce performs one tree/commit/ref round against the current head.
func (t *githubTransaction) commitOnce(
	ctx context.Context, message string, pending map[string]*githubChange, blobs map[string]string,
) error {
	if err := t.store.ensureHead(ctx); err != nil {
		return err
	}

	parentSHA, baseTreeSHA := t.store.headSnapshot()

	entries, err := t.buildTreeEntries(ctx, pending, blobs)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		// Every buffered change was a deletion of a path that is not in the
		// tree; there is nothing to commit.
		return nil
	}

	treeSHA, err := t.store.api.createTree(ctx, baseTreeSHA, entries)
	if err != nil {
		return err
	}

	parents := []string{}
	if parentSHA != "" {
		parents = []string{parentSHA}
	}

	commitSHA, err := t.store.api.createCommit(ctx, message, treeSHA, parents, t.author())
	if err != nil {
		return err
	}

	if parentSHA == "" {
		err = t.store.api.createRef(ctx, t.store.branch, commitSHA)
	} else {
		err = t.store.api.updateRef(ctx, t.store.branch, commitSHA)
	}
	if err != nil {
		return err
	}

	t.store.setHead(commitSHA, treeSHA)
	t.store.api.logRateBudget(ctx)

	return nil
}

// buildTreeEntries converts the buffered change set into tree entries.
//
// Deletions are emitted as entries with a null sha. A deletion of a path that
// is not present in the base tree is dropped: GitHub rejects the whole tree
// creation otherwise.
func (t *githubTransaction) buildTreeEntries(
	ctx context.Context, pending map[string]*githubChange, blobs map[string]string,
) ([]gitHubTreeRequestEntry, error) {
	entries := make([]gitHubTreeRequestEntry, 0, len(pending))

	for _, path := range slices.Sorted(maps.Keys(pending)) {
		if !pending[path].deleted {
			sha := blobs[path]
			entries = append(entries, gitHubTreeRequestEntry{
				Path: path,
				Mode: gitHubBlobMode,
				Type: gitHubTypeBlob,
				SHA:  &sha,
			})
			continue
		}

		existing, found, err := t.store.resolveEntry(ctx, path)
		if err != nil {
			return nil, err
		}
		if !found || existing.Type != gitHubTypeBlob {
			continue
		}

		entries = append(entries, gitHubTreeRequestEntry{
			Path: path,
			Mode: gitHubBlobMode,
			Type: gitHubTypeBlob,
			SHA:  nil,
		})
	}

	return entries, nil
}

// author builds the commit identity from the remote configuration.
func (t *githubTransaction) author() gitHubAuthor {
	cfg := t.store.cfg

	name := defaultCommitUser
	email := defaultCommitEmail
	if cfg != nil && cfg.User != "" {
		name = cfg.User
	}
	if cfg != nil && cfg.Email != "" {
		email = cfg.Email
	}

	return gitHubAuthor{
		Name:  name,
		Email: email,
		Date:  time.Now().UTC().Format(time.RFC3339),
	}
}
