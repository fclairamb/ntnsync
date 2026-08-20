package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// newStagingTestStore creates a local-only store backed by a fresh temporary
// repository, plus a context to drive it with.
func newStagingTestStore(t *testing.T) (context.Context, *LocalStore) {
	t.Helper()

	dir := t.TempDir()

	store, err := NewLocalStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	return context.Background(), store
}

// headContent returns the content committed at path in HEAD, and whether the
// path exists in HEAD at all.
func headContent(t *testing.T, s *LocalStore, path string) (string, bool) {
	t.Helper()

	head, err := s.repo.Head()
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}

	commit, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("read HEAD commit: %v", err)
	}

	file, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return "", false
		}
		t.Fatalf("read %s from HEAD: %v", path, err)
	}

	content, err := file.Contents()
	if err != nil {
		t.Fatalf("read contents of %s: %v", path, err)
	}

	return content, true
}

// headHash returns the current HEAD commit hash, or plumbing.ZeroHash when the
// repository has no commit yet.
func headHash(t *testing.T, s *LocalStore) plumbing.Hash {
	t.Helper()

	head, err := s.repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash
		}
		t.Fatalf("resolve HEAD: %v", err)
	}

	return head.Hash()
}

// writeAndCommit writes a single file through a fresh transaction and commits it.
func writeAndCommit(ctx context.Context, t *testing.T, s *LocalStore, path, content, message string) {
	t.Helper()

	tx, err := s.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	if err := tx.Write(ctx, path, []byte(content)); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if err := tx.Commit(ctx, message); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// bulkTx runs apply against a fresh transaction and commits it.
func bulkTx(ctx context.Context, t *testing.T, s *LocalStore, message string, apply func(tx Transaction)) {
	t.Helper()

	tx, err := s.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	apply(tx)

	if err := tx.Commit(ctx, message); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestTargetedStaging_NewFileReachesHEAD(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "docs/page.md", "hello", "add page")

	got, ok := headContent(t, store, "docs/page.md")
	if !ok {
		t.Fatal("docs/page.md missing from HEAD")
	}
	if got != "hello" {
		t.Fatalf("HEAD content = %q, want %q", got, "hello")
	}
}

func TestTargetedStaging_ModifiedFileReachesHEAD(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "page.md", "first", "add page")
	writeAndCommit(ctx, t, store, "page.md", "second", "update page")

	got, ok := headContent(t, store, "page.md")
	if !ok {
		t.Fatal("page.md missing from HEAD")
	}
	if got != "second" {
		t.Fatalf("HEAD content = %q, want %q", got, "second")
	}
}

func TestTargetedStaging_DeletedFileLeavesHEAD(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "keep.md", "keep", "add keep")
	writeAndCommit(ctx, t, store, "nested/gone.md", "gone", "add gone")

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Delete(ctx, "nested/gone.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := tx.Commit(ctx, "remove gone"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, ok := headContent(t, store, "nested/gone.md"); ok {
		t.Fatal("nested/gone.md still present in HEAD after delete")
	}
	if _, ok := headContent(t, store, "keep.md"); !ok {
		t.Fatal("keep.md was dropped from HEAD by an unrelated delete")
	}
}

func TestTargetedStaging_WriteThenDeleteSamePath(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	// An initial commit so HEAD exists and the no-op case is distinguishable.
	writeAndCommit(ctx, t, store, "base.md", "base", "add base")
	before := headHash(t, store)

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Write(ctx, "temp/scratch.md", []byte("transient")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Delete(ctx, "temp/scratch.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The path was never in the index, so Remove must be tolerated.
	if err := tx.Commit(ctx, "transient page"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := headHash(t, store); got != before {
		t.Fatalf("commit created a new commit %s for a write-then-delete; HEAD was %s", got, before)
	}
	if _, ok := headContent(t, store, "temp/scratch.md"); ok {
		t.Fatal("temp/scratch.md must not be in HEAD")
	}
}

func TestTargetedStaging_NoChangesIsNoOp(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "page.md", "content", "add page")
	before := headHash(t, store)

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Commit(ctx, "nothing to do"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := headHash(t, store); got != before {
		t.Fatalf("empty transaction created commit %s, HEAD was %s", got, before)
	}
}

func TestTargetedStaging_RewriteWithIdenticalContentIsNoOp(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "page.md", "same", "add page")
	before := headHash(t, store)

	// The path lands in modifiedPaths but its content matches HEAD, so staging
	// it must not be counted as a change.
	writeAndCommit(ctx, t, store, "page.md", "same", "rewrite page")

	if got := headHash(t, store); got != before {
		t.Fatalf("identical rewrite created empty commit %s, HEAD was %s", got, before)
	}
}

func TestTargetedStaging_AboveThresholdFallsBackToWholeTree(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	count := maxTargetedStagingPaths + 1

	bulkTx(ctx, t, store, "bulk import", func(tx Transaction) {
		for i := range count {
			path := fmt.Sprintf("bulk/page-%04d.md", i)
			if err := tx.Write(ctx, path, fmt.Appendf(nil, "body %d", i)); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
	})

	for _, i := range []int{0, count / 2, count - 1} {
		path := fmt.Sprintf("bulk/page-%04d.md", i)
		got, ok := headContent(t, store, path)
		if !ok {
			t.Fatalf("%s missing from HEAD", path)
		}
		if want := fmt.Sprintf("body %d", i); got != want {
			t.Fatalf("HEAD content of %s = %q, want %q", path, got, want)
		}
	}

	// A delete on the fallback path must also reach HEAD.
	bulkTx(ctx, t, store, "bulk removal", func(tx Transaction) {
		for i := range count {
			path := fmt.Sprintf("bulk/page-%04d.md", i)
			if err := tx.Delete(ctx, path); err != nil {
				t.Fatalf("delete %s: %v", path, err)
			}
		}
	})

	if _, ok := headContent(t, store, "bulk/page-0000.md"); ok {
		t.Fatal("bulk/page-0000.md still present in HEAD after bulk delete")
	}
}

func TestTargetedStaging_UntrackedWorktreeFileIsNotStaged(t *testing.T) {
	t.Parallel()

	ctx, store := newStagingTestStore(t)

	writeAndCommit(ctx, t, store, "page.md", "content", "add page")
	before := headHash(t, store)

	// A file that appeared in the worktree without going through the
	// transaction is not part of modifiedPaths and must not be picked up.
	stray := store.rootPath + string(os.PathSeparator) + "stray.md"
	if err := os.WriteFile(stray, []byte("stray"), filePerm); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Commit(ctx, "should be a no-op"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := headHash(t, store); got != before {
		t.Fatalf("stray worktree file produced commit %s, HEAD was %s", got, before)
	}
}
