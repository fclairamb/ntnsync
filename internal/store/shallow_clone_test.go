package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
)

func boolPtr(b bool) *bool { return &b }

// newTestRemoteConfig returns a RemoteConfig suitable for exercising a
// LocalStore against a local bare "remote" repository at barePath.
func newTestRemoteConfig(barePath string, depth int) *RemoteConfig {
	return &RemoteConfig{
		URL:     barePath,
		Branch:  defaultBranch,
		User:    "tester",
		Email:   "tester@example.com",
		Push:    boolPtr(true),
		Depth:   depth,
		Storage: StorageModeRemote,
		// GetAuth() requires a non-empty Password for non-SSH URLs. The local
		// "file" transport used here (a plain filesystem path) shells out to
		// the system git binary and does not actually use it.
		Password: "unused",
	}
}

// commitFile writes content to path in the store and commits it.
func commitFile(ctx context.Context, t *testing.T, s *LocalStore, path, content, message string) {
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

// readTestFile reads a file written into a test store's working directory.
func readTestFile(t *testing.T, storePath, name string) string {
	t.Helper()

	full := filepath.Join(storePath, name)
	got, err := os.ReadFile(full) //nolint:gosec // path is test-controlled
	if err != nil {
		t.Fatalf("read %s: %v", full, err)
	}
	return string(got)
}

// TestShallowClone_ClonePushPull verifies the full clone -> add -> commit ->
// push -> pull cycle works when the local clone is shallow (Depth: 1), which
// is now the default (NTN_GIT_DEPTH unset). This is the risk area flagged in
// specs/todos/2026-08-20-git-shallow-clone.md: Pull/fetchAndMergeLocked were
// previously unverified against a shallow clone.
func TestShallowClone_ClonePushPull(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	barePath := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(barePath, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	// Seed the bare "remote" with an initial commit so the second store's
	// clone takes the real git.PlainClone(...) code path (with Depth) rather
	// than the empty-repo/init-locally fallback.
	seedPath := filepath.Join(t.TempDir(), "seed")
	seedStore, err := NewLocalStore(seedPath,
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

	// Consumer clones the now-non-empty remote with a shallow depth of 1.
	consumerPath := filepath.Join(t.TempDir(), "consumer")
	consumerCfg := newTestRemoteConfig(barePath, 1)
	consumerStore, err := NewLocalStore(consumerPath,
		WithRemoteConfig(consumerCfg),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("shallow clone: %v", err)
	}

	shallows, err := consumerStore.repo.Storer.Shallow()
	if err != nil {
		t.Fatalf("read shallow info: %v", err)
	}
	if len(shallows) == 0 {
		t.Fatalf("expected clone with Depth:1 to be shallow, but Shallow() returned none")
	}

	// add -> commit -> push from the shallow clone.
	commitFile(ctx, t, consumerStore, "consumer.txt", "hello from consumer", "consumer commit")
	if pushErr := consumerStore.Push(ctx); pushErr != nil {
		t.Fatalf("push from shallow clone: %v", pushErr)
	}

	// A separate publisher clones fresh (full depth) from the bare remote,
	// which now includes the consumer's commit, and pushes one more commit.
	publisherPath := filepath.Join(t.TempDir(), "publisher")
	publisherStore, err := NewLocalStore(publisherPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("create publisher store: %v", err)
	}
	commitFile(ctx, t, publisherStore, "publisher.txt", "hello from publisher", "publisher commit")
	if pushErr := publisherStore.Push(ctx); pushErr != nil {
		t.Fatalf("publisher push: %v", pushErr)
	}

	// The shallow consumer should be able to pull the publisher's new commit
	// (fast-forward path through pullLocked/PullOptions.Depth).
	if pullErr := consumerStore.Pull(ctx); pullErr != nil {
		t.Fatalf("pull on shallow clone: %v", pullErr)
	}

	if got := readTestFile(t, consumerPath, "publisher.txt"); got != "hello from publisher" {
		t.Fatalf("pulled file content = %q, want %q", got, "hello from publisher")
	}
}

// TestShallowClone_PullDiverged verifies the fetchAndMergeLocked reset path
// (triggered by a non-fast-forward pull) also works against a shallow clone.
func TestShallowClone_PullDiverged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	barePath := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(barePath, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	seedPath := filepath.Join(t.TempDir(), "seed")
	seedStore, err := NewLocalStore(seedPath,
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

	// Consumer clones shallow.
	consumerPath := filepath.Join(t.TempDir(), "consumer")
	consumerStore, err := NewLocalStore(consumerPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 1)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("shallow clone: %v", err)
	}

	// Consumer makes a local commit but does NOT push it, so it diverges from
	// what will be pushed to the remote next.
	commitFile(ctx, t, consumerStore, "local-only.txt", "local", "local commit")

	// A publisher pushes a different, unrelated commit to the remote.
	publisherPath := filepath.Join(t.TempDir(), "publisher")
	publisherStore, err := NewLocalStore(publisherPath,
		WithRemoteConfig(newTestRemoteConfig(barePath, 0)),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("create publisher store: %v", err)
	}
	commitFile(ctx, t, publisherStore, "publisher.txt", "hello from publisher", "publisher commit")
	if pushErr := publisherStore.Push(ctx); pushErr != nil {
		t.Fatalf("publisher push: %v", pushErr)
	}

	// Consumer's branch has now diverged from origin/main: pulling must go
	// through fetchAndMergeLocked (fetch + hard reset to remote).
	if pullErr := consumerStore.Pull(ctx); pullErr != nil {
		t.Fatalf("pull on diverged shallow clone: %v", pullErr)
	}

	if got := readTestFile(t, consumerPath, "publisher.txt"); got != "hello from publisher" {
		t.Fatalf("pulled file content = %q, want %q", got, "hello from publisher")
	}
}
