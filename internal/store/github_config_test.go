package store

import (
	"errors"
	"testing"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

func TestParseGitHubRepo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   error
	}{
		{name: "https with .git", url: "https://github.com/acme/wiki.git", wantOwner: "acme", wantRepo: "wiki"},
		{name: "https without .git", url: "https://github.com/acme/wiki", wantOwner: "acme", wantRepo: "wiki"},
		{name: "https trailing slash", url: "https://github.com/acme/wiki/", wantOwner: "acme", wantRepo: "wiki"},
		{ //nolint:gosec // fixture URL, no real credential
			name: "https with credentials", url: "https://user:token@github.com/acme/wiki.git",
			wantOwner: "acme", wantRepo: "wiki",
		},
		{name: "scp style", url: "git@github.com:acme/wiki.git", wantOwner: "acme", wantRepo: "wiki"},
		{name: "ssh scheme", url: "ssh://git@github.com/acme/wiki.git", wantOwner: "acme", wantRepo: "wiki"},
		{name: "uppercase host", url: "https://GitHub.com/acme/wiki", wantOwner: "acme", wantRepo: "wiki"},
		{name: "gitlab", url: "https://gitlab.com/acme/wiki.git", wantErr: apperrors.ErrNotGitHubURL},
		{name: "enterprise host", url: "https://github.acme.com/acme/wiki.git", wantErr: apperrors.ErrNotGitHubURL},
		{name: "missing repo", url: "https://github.com/acme", wantErr: apperrors.ErrNotGitHubURL},
		{name: "too deep", url: "https://github.com/acme/wiki/extra", wantErr: apperrors.ErrNotGitHubURL},
		{name: "empty", url: "", wantErr: apperrors.ErrRemoteNotConfiguredSetURL},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			owner, repo, err := ParseGitHubRepo(testCase.url)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("expected error %v, got %v", testCase.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != testCase.wantOwner || repo != testCase.wantRepo {
				t.Fatalf("expected %s/%s, got %s/%s", testCase.wantOwner, testCase.wantRepo, owner, repo)
			}
		})
	}
}

func TestGitHubStorageModeSelection(t *testing.T) {
	t.Parallel()

	cfg := &RemoteConfig{Storage: StorageModeGitHub, URL: "https://github.com/acme/wiki.git"}

	if cfg.EffectiveStorageMode() != StorageModeGitHub {
		t.Fatalf("expected github mode, got %q", cfg.EffectiveStorageMode())
	}
	if !cfg.IsGitHubAPI() {
		t.Fatal("expected IsGitHubAPI to be true")
	}
	if !cfg.IsEnabled() {
		t.Fatal("expected remote operations to be enabled in github mode")
	}

	local := &RemoteConfig{Storage: StorageModeLocal, URL: "https://github.com/acme/wiki.git"}
	if local.IsGitHubAPI() {
		t.Fatal("local mode must not select the github backend")
	}

	auto := &RemoteConfig{URL: "https://github.com/acme/wiki.git"}
	if auto.IsGitHubAPI() {
		t.Fatal("github mode must be opt-in, never auto-detected")
	}
	if auto.EffectiveStorageMode() != StorageModeRemote {
		t.Fatalf("auto-detection must still yield remote, got %q", auto.EffectiveStorageMode())
	}
}

func TestNewGitHubStoreFailsFastOnBadConfig(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()

	t.Run("non github url", func(t *testing.T) {
		t.Parallel()

		cfg := testRemoteConfig()
		cfg.URL = "https://gitlab.com/acme/wiki.git"

		_, err := newTestGitHubStore(fake, cfg)
		if !errors.Is(err, apperrors.ErrNotGitHubURL) {
			t.Fatalf("expected ErrNotGitHubURL, got %v", err)
		}
	})

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()

		cfg := testRemoteConfig()
		cfg.Password = ""

		_, err := newTestGitHubStore(fake, cfg)
		if !errors.Is(err, apperrors.ErrGitHubTokenRequired) {
			t.Fatalf("expected ErrGitHubTokenRequired, got %v", err)
		}
	})

	t.Run("missing url", func(t *testing.T) {
		t.Parallel()

		cfg := testRemoteConfig()
		cfg.URL = ""

		_, err := newTestGitHubStore(fake, cfg)
		if !errors.Is(err, apperrors.ErrRemoteNotConfiguredSetURL) {
			t.Fatalf("expected ErrRemoteNotConfiguredSetURL, got %v", err)
		}
	})
}
