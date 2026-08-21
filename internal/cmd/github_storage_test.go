package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/store"
)

// useGitHubStorage points the process at a github-mode configuration.
// It never talks to the network: every command under test fails before any
// API call is made.
func useGitHubStorage(t *testing.T, url string) {
	t.Helper()

	t.Setenv("NTN_STORAGE", "github")
	t.Setenv("NTN_GIT_URL", url)
	t.Setenv("NTN_GIT_PASS", "test-token")
	t.Setenv("NTN_GIT_BRANCH", "main")
	t.Setenv("NTN_QUEUE_BRANCH", "")
	t.Setenv("NTN_DIR", t.TempDir())
}

// TestWholeTreeCommandsFailFastInGitHubMode covers the spec requirement that
// reindex, cleanup and list --tree error out naming the mode instead of
// half-working.
//
//nolint:paralleltest // t.Setenv forbids parallel execution
func TestWholeTreeCommandsFailFastInGitHubMode(t *testing.T) {
	cases := [][]string{
		{"ntnsync", cmdNameReindex},
		{"ntnsync", cmdNameReindex, "--" + flagCompact},
		{"ntnsync", "cleanup", "--dry-run"},
		{"ntnsync", "list", "--tree"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			useGitHubStorage(t, "https://github.com/acme/wiki.git")

			err := NewApp().Run(context.Background(), args)
			if err == nil {
				t.Fatalf("%v must fail in github mode", args)
			}
			if !errors.Is(err, apperrors.ErrGitHubWholeTreeUnsupported) {
				t.Fatalf("expected ErrGitHubWholeTreeUnsupported, got %v", err)
			}
			if !strings.Contains(err.Error(), "NTN_STORAGE=github") {
				t.Fatalf("the error must name the storage mode, got %q", err)
			}
		})
	}
}

//nolint:paralleltest // t.Setenv forbids parallel execution
func TestGitHubModeRejectsNonGitHubURLAtStartup(t *testing.T) {
	useGitHubStorage(t, "https://gitlab.com/acme/wiki.git")

	err := NewApp().Run(context.Background(), []string{"ntnsync", cmdNameStatus})
	if !errors.Is(err, apperrors.ErrNotGitHubURL) {
		t.Fatalf("expected ErrNotGitHubURL, got %v", err)
	}
}

func TestGitHubModeRequiresAToken(t *testing.T) {
	useGitHubStorage(t, "https://github.com/acme/wiki.git")
	t.Setenv("NTN_GIT_PASS", "")

	err := NewApp().Run(context.Background(), []string{"ntnsync", cmdNameStatus})
	if !errors.Is(err, apperrors.ErrGitHubTokenRequired) {
		t.Fatalf("expected ErrGitHubTokenRequired, got %v", err)
	}
}

//nolint:paralleltest // t.Setenv forbids parallel execution
func TestCreateStoreSelectsTheGitHubBackend(t *testing.T) {
	useGitHubStorage(t, "https://github.com/acme/wiki.git")

	cfg := store.LoadRemoteConfigFromEnv()

	storeInst, err := createGitHubStore(cfg)
	if err != nil {
		t.Fatalf("create github store: %v", err)
	}

	if _, ok := storeInst.(*store.GitHubStore); !ok {
		t.Fatalf("expected a *store.GitHubStore, got %T", storeInst)
	}

	if storeRemoteConfig(storeInst) != cfg {
		t.Fatal("storeRemoteConfig must resolve the config through the RemoteStore interface")
	}

	if err := storePull(context.Background(), nonRemoteStore{}); err != nil {
		t.Fatalf("a store without remote support must be a no-op pull: %v", err)
	}
	if storeRemoteConfig(nonRemoteStore{}) != nil {
		t.Fatal("a store without remote support must report no remote config")
	}
	if err := ensureWholeTreeSupported(nonRemoteStore{}, "reindex"); err != nil {
		t.Fatalf("a store without the capability probe must be treated as capable: %v", err)
	}
}

func TestCreateStoreBuildsASplitStoreForTheQueueBranch(t *testing.T) {
	useGitHubStorage(t, "https://github.com/acme/wiki.git")
	t.Setenv("NTN_QUEUE_BRANCH", "queue")

	cfg := store.LoadRemoteConfigFromEnv()

	storeInst, err := createGitHubStore(cfg)
	if err != nil {
		t.Fatalf("create github store: %v", err)
	}

	split, ok := storeInst.(*store.SplitStore)
	if !ok {
		t.Fatalf("expected a *store.SplitStore, got %T", storeInst)
	}

	content, ok := split.ContentStore().(*store.GitHubStore)
	if !ok {
		t.Fatalf("expected a GitHub content store, got %T", split.ContentStore())
	}
	if content.Branch() != "main" {
		t.Fatalf("content store must target main, got %q", content.Branch())
	}

	queue, ok := split.QueueStore().(*store.GitHubStore)
	if !ok {
		t.Fatalf("expected a GitHub queue store, got %T", split.QueueStore())
	}
	if queue.Branch() != "queue" {
		t.Fatalf("queue store must target the queue branch, got %q", queue.Branch())
	}
}

// nonRemoteStore is a Store that implements neither RemoteStore nor
// WholeTreeChecker, exercising the fallback branches of the helpers.
type nonRemoteStore struct{ store.Store }
