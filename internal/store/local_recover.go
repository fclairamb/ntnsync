package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// recloneMarker is written inside .git when the clone cannot be recovered in
// place. The next NewLocalStore wipes the directory and clones again.
const recloneMarker = "ntnsync-reclone"

// isDivergedPushErr reports whether a push failed because the remote branch
// moved. On a shallow clone go-git's fast-forward check walks our history down
// to the shallow boundary and fails with "object not found" instead of
// returning a non-fast-forward error, so both are treated the same way.
func isDivergedPushErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, git.ErrNonFastForwardUpdate) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "object not found")
}

// remoteTrackingHash returns the last remote state we saw (clone, fetch or
// successful push), or the zero hash when there is none.
func (s *LocalStore) remoteTrackingHash() plumbing.Hash {
	ref, err := s.repo.Reference(plumbing.NewRemoteReferenceName(gitRemoteOrigin, s.remoteConfig.Branch), true)
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// integrateRemoteLocked fetches the remote branch and replays the local changes
// made since baseHash on top of it, so a commit pushed by someone else
// (Renovate, a human) never blocks the sync. Caller must hold s.mu.
//
// The local commits are squashed into a single commit whose parent is the
// remote tip. Its tree is HEAD's tree plus the remote changes to paths we did
// not touch: on a path both sides changed, ours wins, since ntnsync's copy is
// the freshest export of the Notion page.
//
// baseHash is the merge base of both sides: the remote-tracking ref as it was
// before anything fetched the new remote tip.
func (s *LocalStore) integrateRemoteLocked(
	ctx context.Context, auth transport.AuthMethod, baseHash plumbing.Hash,
) error {
	head, err := s.repo.Head()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}

	theirsHash, err := s.fetchRemoteTip(ctx, auth)
	if err != nil {
		return err
	}

	if theirsHash == baseHash || theirsHash == head.Hash() {
		// The remote did not move: whatever broke the push is local.
		return fmt.Errorf("%w: remote branch did not move", apperrors.ErrLocalRepoBroken)
	}

	worktree, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	stats, err := s.stageRemoteChanges(ctx, worktree, baseHash, head.Hash(), theirsHash)
	if err != nil {
		return err
	}

	// The tree comes from the index: HEAD plus the remote changes staged above.
	// Uncommitted writes of an open transaction are not staged, so they stay in
	// the worktree for that transaction's next commit.
	newHash, err := worktree.Commit(
		fmt.Sprintf("[ntnsync] sync (rebased onto %s)", theirsHash.String()[:7]),
		&git.CommitOptions{
			Author: &object.Signature{
				Name:  s.remoteConfig.User,
				Email: s.remoteConfig.Email,
				When:  time.Now(),
			},
			Parents:           []plumbing.Hash{theirsHash},
			AllowEmptyCommits: true,
		})
	if err != nil {
		return fmt.Errorf("commit rebased changes: %w", err)
	}

	s.logger.InfoContext(ctx, "integrated remote changes",
		"remote_commit", theirsHash.String()[:7],
		"local_changes", stats.ours,
		"remote_changes", stats.theirs,
		"remote_changes_applied", stats.applied,
		"commit", newHash.String()[:7])

	return nil
}

// fetchRemoteTip fetches the configured branch and returns its tip.
func (s *LocalStore) fetchRemoteTip(ctx context.Context, auth transport.AuthMethod) (plumbing.Hash, error) {
	remoteRefName := plumbing.NewRemoteReferenceName(gitRemoteOrigin, s.remoteConfig.Branch)

	err := s.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: gitRemoteOrigin,
		Auth:       auth,
		Depth:      s.remoteConfig.Depth,
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("+refs/heads/%s:%s", s.remoteConfig.Branch, remoteRefName)),
		},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return plumbing.ZeroHash, fmt.Errorf("fetch: %w", err)
	}

	ref, err := s.repo.Reference(remoteRefName, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("get remote ref: %w", err)
	}
	return ref.Hash(), nil
}

// integrateStats counts the changes seen while integrating the remote branch.
type integrateStats struct {
	ours    int // paths changed locally since the base
	theirs  int // paths changed on the remote since the base
	applied int // remote changes applied (not overridden by a local change)
}

// stageRemoteChanges writes and stages every remote change since baseHash to a
// path the local side did not change since baseHash.
func (s *LocalStore) stageRemoteChanges(
	ctx context.Context, worktree *git.Worktree, baseHash, oursHash, theirsHash plumbing.Hash,
) (integrateStats, error) {
	var stats integrateStats

	baseTree, err := s.commitTree(baseHash)
	if err != nil {
		return stats, fmt.Errorf("read base tree: %w", err)
	}
	oursTree, err := s.commitTree(oursHash)
	if err != nil {
		return stats, fmt.Errorf("read HEAD tree: %w", err)
	}
	theirsTree, err := s.commitTree(theirsHash)
	if err != nil {
		return stats, fmt.Errorf("read remote tree: %w", err)
	}

	ourChanges, err := object.DiffTreeWithOptions(ctx, baseTree, oursTree, nil)
	if err != nil {
		return stats, fmt.Errorf("diff local changes: %w", err)
	}
	theirChanges, err := object.DiffTreeWithOptions(ctx, baseTree, theirsTree, nil)
	if err != nil {
		return stats, fmt.Errorf("diff remote changes: %w", err)
	}
	stats.ours, stats.theirs = len(ourChanges), len(theirChanges)

	ours := make(map[string]bool, len(ourChanges))
	for _, change := range ourChanges {
		ours[changePath(change)] = true
	}

	for _, change := range theirChanges {
		if ours[changePath(change)] {
			continue
		}
		if applyErr := s.applyRemoteChange(worktree, theirsTree, change); applyErr != nil {
			return stats, applyErr
		}
		stats.applied++
	}

	return stats, nil
}

// commitTree returns the tree of a commit, or an empty tree for the zero hash
// (no known remote state).
func (s *LocalStore) commitTree(hash plumbing.Hash) (*object.Tree, error) {
	if hash.IsZero() {
		return &object.Tree{}, nil
	}
	commit, err := s.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", hash, err)
	}
	return commit.Tree()
}

// changePath returns the path a tree change applies to.
func changePath(change *object.Change) string {
	if change.To.Name != "" {
		return change.To.Name
	}
	return change.From.Name
}

// applyRemoteChange writes one remote change to the worktree and stages it.
func (s *LocalStore) applyRemoteChange(worktree *git.Worktree, theirsTree *object.Tree, change *object.Change) error {
	gitPath := changePath(change)
	fullPath := filepath.Join(s.rootPath, filepath.FromSlash(gitPath))

	if change.To.Name == "" {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", gitPath, err)
		}
		if _, err := worktree.Remove(gitPath); err != nil {
			return fmt.Errorf("git rm %s: %w", gitPath, err)
		}
		return nil
	}

	file, err := theirsTree.TreeEntryFile(&change.To.TreeEntry)
	if err != nil {
		return fmt.Errorf("read remote file %s: %w", gitPath, err)
	}

	if err := writeBlobFile(file, fullPath); err != nil {
		return fmt.Errorf("write %s: %w", gitPath, err)
	}

	if err := worktree.AddWithOptions(&git.AddOptions{Path: gitPath, SkipStatus: true}); err != nil {
		return fmt.Errorf("git add %s: %w", gitPath, err)
	}
	return nil
}

// writeBlobFile writes a git file object to fullPath, keeping its executable bit.
func writeBlobFile(file *object.File, fullPath string) error {
	if err := os.MkdirAll(filepath.Dir(fullPath), dirPerm); err != nil {
		return err
	}

	perm := os.FileMode(filePerm)
	if file.Mode == filemode.Executable {
		perm = dirPerm
	}

	reader, err := file.Reader()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	//nolint:gosec // path comes from a git tree
	out, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// flagForReclone marks the clone so the next NewLocalStore replaces it.
func (s *LocalStore) flagForReclone(ctx context.Context, cause error) {
	marker := filepath.Join(s.rootPath, git.GitDirName, recloneMarker)
	if err := os.WriteFile(marker, []byte(cause.Error()+"\n"), filePerm); err != nil {
		s.logger.ErrorContext(ctx, "failed to flag repository for re-clone", "error", err)
		return
	}
	s.logger.ErrorContext(ctx, "local repository flagged for re-clone on next start", "cause", cause)
}

// checkRepoHealth returns why an existing clone cannot be used, or nil.
func checkRepoHealth(path string) error {
	marker := filepath.Join(path, git.GitDirName, recloneMarker)
	if data, err := os.ReadFile(marker); err == nil { //nolint:gosec // fixed path under the store
		return fmt.Errorf("%w: %s", apperrors.ErrLocalRepoBroken, strings.TrimSpace(string(data)))
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil // no commit yet
		}
		return fmt.Errorf("resolve HEAD: %w", err)
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return fmt.Errorf("read HEAD commit: %w", err)
	}
	if _, err := commit.Tree(); err != nil {
		return fmt.Errorf("read HEAD tree: %w", err)
	}
	return nil
}
