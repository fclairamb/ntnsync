package store

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
)

// TestShallowClone_PushAfterForeignCommit reproduces the Stonal crash loop of
// 2026-10-12: a shallow clone commits locally while another writer (Renovate)
// pushes to the same branch. go-git's fast-forward check walks our history,
// hits the shallow boundary and fails with "object not found" instead of a
// non-fast-forward error, so Push never recovered.
func TestShallowClone_PushAfterForeignCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	barePath := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(barePath, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	// Two commits on the remote, so a depth-1 clone has a missing parent.
	seedStore, err := NewLocalStore(filepath.Join(t.TempDir(), "seed"),
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithCreateBranchIfMissing(),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("create seed store: %v", err)
	}
	commitFile(ctx, t, seedStore, "seed.txt", "seed", "seed commit 1")
	commitFile(ctx, t, seedStore, "seed.txt", "seed 2", "seed commit 2")
	if pushErr := seedStore.Push(ctx); pushErr != nil {
		t.Fatalf("seed push: %v", pushErr)
	}

	consumerPath := filepath.Join(t.TempDir(), "consumer")
	consumerStore, err := NewLocalStore(consumerPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 1)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("shallow clone: %v", err)
	}

	// The consumer pushes once, like ntnsync's 09:35:50 commit.
	commitFile(ctx, t, consumerStore, "page-a.md", "a", "sync a")
	if pushErr := consumerStore.Push(ctx); pushErr != nil {
		t.Fatalf("first consumer push: %v", pushErr)
	}

	// A foreign writer pushes on top, like Renovate at 09:36:11.
	foreignPath := filepath.Join(t.TempDir(), "foreign")
	foreignStore, err := NewLocalStore(foreignPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("create foreign store: %v", err)
	}
	commitFile(ctx, t, foreignStore, "renovate.json", "{}", "chore(renovate): config")
	// A path we also change below (ours must win) and a deletion of a path we
	// never touch (must be applied).
	commitFile(ctx, t, foreignStore, "page-a.md", "foreign", "foreign edit")
	commitFile(ctx, t, foreignStore, "other.md", "other", "foreign add")
	tx, err := foreignStore.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if delErr := tx.Delete(ctx, "seed.txt"); delErr != nil {
		t.Fatalf("delete seed.txt: %v", delErr)
	}
	if commitErr := tx.Commit(ctx, "foreign delete"); commitErr != nil {
		t.Fatalf("commit delete: %v", commitErr)
	}
	if pushErr := foreignStore.Push(ctx); pushErr != nil {
		t.Fatalf("foreign push: %v", pushErr)
	}

	// The consumer commits twice more without pulling, then pushes.
	commitFile(ctx, t, consumerStore, "page-b.md", "b", "sync b")
	commitFile(ctx, t, consumerStore, "page-a.md", "a2", "sync a2")
	if pushErr := consumerStore.Push(ctx); pushErr != nil {
		t.Fatalf("push after foreign commit: %v", pushErr)
	}

	// The remote must keep both the foreign commit and every consumer change.
	checkPath := filepath.Join(t.TempDir(), "check")
	checkStore, err := NewLocalStore(checkPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("clone check: %v", err)
	}
	for name, want := range map[string]string{
		"renovate.json": "{}",
		"other.md":      "other",
		"page-a.md":     "a2",
		"page-b.md":     "b",
	} {
		if got := readTestFile(t, checkPath, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if exists, _ := checkStore.Exists(ctx, "seed.txt"); exists {
		t.Errorf("seed.txt deleted on the remote came back")
	}

	// The consumer can keep going: a later commit pushes normally.
	commitFile(ctx, t, consumerStore, "page-c.md", "c", "sync c")
	if pushErr := consumerStore.Push(ctx); pushErr != nil {
		t.Fatalf("push after recovery: %v", pushErr)
	}
}

var errTestBroken = errors.New("test: broken clone")

// TestLocalStore_ReclonesFlaggedRepo verifies that a clone flagged as broken
// is replaced by a fresh clone on the next start.
func TestLocalStore_ReclonesFlaggedRepo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	barePath := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(barePath, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	seedStore, err := NewLocalStore(filepath.Join(t.TempDir(), "seed"),
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithCreateBranchIfMissing(),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("create seed store: %v", err)
	}
	commitFile(ctx, t, seedStore, "seed.txt", "seed", "seed commit")
	if pushErr := seedStore.Push(ctx); pushErr != nil {
		t.Fatalf("seed push: %v", pushErr)
	}

	path := filepath.Join(t.TempDir(), "consumer")
	consumer, err := NewLocalStore(path,
		WithRemoteConfig(newTestRemoteConfig(barePath, 1)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	commitFile(ctx, t, consumer, "unpushed.md", "x", "unpushed")
	consumer.flagForReclone(ctx, errTestBroken)

	if _, err := NewLocalStore(path,
		WithRemoteConfig(newTestRemoteConfig(barePath, 1)),
		WithLogger(slog.New(slog.DiscardHandler))); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	if _, err := os.Stat(filepath.Join(path, "unpushed.md")); !os.IsNotExist(err) {
		t.Errorf("unpushed.md survived the re-clone (stat err: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".git", recloneMarker)); !os.IsNotExist(err) {
		t.Errorf("re-clone marker survived (stat err: %v)", err)
	}
	if got := readTestFile(t, path, "seed.txt"); got != "seed" {
		t.Errorf("seed.txt = %q, want %q", got, "seed")
	}
}
