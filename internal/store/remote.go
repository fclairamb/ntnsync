package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// StorageMode defines the storage mode for git operations.
type StorageMode string

const (
	// StorageModeAuto automatically detects the storage mode based on configuration.
	StorageModeAuto StorageMode = ""
	// StorageModeLocal uses local-only storage (no remote operations).
	StorageModeLocal StorageMode = "local"
	// StorageModeRemote uses remote storage (pull/push enabled).
	StorageModeRemote StorageMode = "remote"
)

// RemoteConfig holds configuration for remote git operations.
type RemoteConfig struct {
	Storage      StorageMode   // Storage mode: "local", "remote", or auto-detect (NTN_STORAGE)
	URL          string        // Remote git repository URL (NTN_GIT_URL)
	Password     string        // Password/token for HTTPS auth (NTN_GIT_PASS)
	Branch       string        // Target branch (NTN_GIT_BRANCH)
	User         string        // Commit author name (NTN_GIT_USER)
	Email        string        // Commit author email (NTN_GIT_EMAIL)
	Commit       bool          // Enable automatic git commit (NTN_COMMIT)
	CommitPeriod time.Duration // Periodic commit interval during sync (NTN_COMMIT_PERIOD)
	Push         *bool         // Push to remote after commits (NTN_PUSH), nil means auto-detect
}

// LoadRemoteConfigFromEnv loads remote configuration from environment variables.
func LoadRemoteConfigFromEnv() *RemoteConfig {
	cfg := &RemoteConfig{
		Storage:  StorageMode(strings.ToLower(os.Getenv("NTN_STORAGE"))),
		URL:      os.Getenv("NTN_GIT_URL"),
		Password: os.Getenv("NTN_GIT_PASS"),
		Branch:   os.Getenv("NTN_GIT_BRANCH"),
		User:     os.Getenv("NTN_GIT_USER"),
		Email:    os.Getenv("NTN_GIT_EMAIL"),
	}

	// Apply defaults
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.User == "" {
		cfg.User = "ntnsync"
	}
	if cfg.Email == "" {
		cfg.Email = "ntnsync@local"
	}

	// Parse NTN_COMMIT_PERIOD (implicitly enables commit if set)
	if periodStr := os.Getenv("NTN_COMMIT_PERIOD"); periodStr != "" && periodStr != "0" {
		if d, err := time.ParseDuration(periodStr); err == nil && d > 0 {
			cfg.CommitPeriod = d
			cfg.Commit = true // NTN_COMMIT_PERIOD implicitly enables commits
		}
	} else if cfg.URL != "" {
		// Default commit period: 1 minute when remote is configured
		cfg.CommitPeriod = 1 * time.Minute
	}

	// Parse NTN_COMMIT (explicit setting overrides implicit from period)
	if commitStr := os.Getenv("NTN_COMMIT"); commitStr != "" {
		cfg.Commit = parseBoolEnv(commitStr)
		// If commit is enabled but no period is set, use default period
		if cfg.Commit && cfg.CommitPeriod == 0 && cfg.URL != "" {
			cfg.CommitPeriod = 1 * time.Minute
		}
	} else if cfg.CommitPeriod > 0 {
		// CommitPeriod implicitly enables commits
		cfg.Commit = true
	}

	// Parse NTN_PUSH (nil means auto-detect based on NTN_GIT_URL)
	if pushStr := os.Getenv("NTN_PUSH"); pushStr != "" {
		push := parseBoolEnv(pushStr)
		cfg.Push = &push
	}

	return cfg
}

// parseBoolEnv parses a boolean environment variable value.
func parseBoolEnv(val string) bool {
	val = strings.ToLower(val)
	return val == "true" || val == "1" || val == "yes"
}

// EffectiveStorageMode returns the effective storage mode after auto-detection.
// If Storage is set explicitly, it returns that value.
// Otherwise, it returns "remote" if URL is configured, or "local" if not.
func (c *RemoteConfig) EffectiveStorageMode() StorageMode {
	if c == nil {
		return StorageModeLocal
	}
	if c.Storage == StorageModeLocal || c.Storage == StorageModeRemote {
		return c.Storage
	}
	// Auto-detect: use remote if URL is configured
	if c.URL != "" {
		return StorageModeRemote
	}
	return StorageModeLocal
}

// IsEnabled returns true if remote operations should be used.
// This checks both the storage mode and whether a URL is configured.
func (c *RemoteConfig) IsEnabled() bool {
	if c == nil {
		return false
	}
	// If explicitly set to local, remote is disabled
	if c.Storage == StorageModeLocal {
		return false
	}
	// Remote requires a URL
	return c.URL != ""
}

// IsSSH returns true if the URL is an SSH URL.
func (c *RemoteConfig) IsSSH() bool {
	if c == nil || c.URL == "" {
		return false
	}
	return strings.HasPrefix(c.URL, "git@") || strings.HasPrefix(c.URL, "ssh://")
}

// IsCommitEnabled returns true if automatic commits are enabled.
func (c *RemoteConfig) IsCommitEnabled() bool {
	if c == nil {
		return false
	}
	return c.Commit
}

// IsPushEnabled returns true if push to remote is enabled.
// When NTN_PUSH is not explicitly set, defaults to true if NTN_GIT_URL is set.
func (c *RemoteConfig) IsPushEnabled() bool {
	if c == nil {
		return false
	}
	if c.Push != nil {
		return *c.Push
	}
	// Default: true when NTN_GIT_URL is set, false otherwise
	return c.URL != ""
}

// GetCommitPeriod returns the periodic commit interval.
func (c *RemoteConfig) GetCommitPeriod() time.Duration {
	if c == nil {
		return 0
	}
	return c.CommitPeriod
}

// GetAuth returns the appropriate authentication method for the remote URL.
func (c *RemoteConfig) GetAuth() (transport.AuthMethod, error) {
	if c == nil || c.URL == "" {
		return nil, apperrors.ErrRemoteNotConfigured
	}

	if c.IsSSH() {
		auth, err := ssh.NewSSHAgentAuth("git")
		if err != nil {
			return nil, fmt.Errorf("create SSH agent auth: %w", err)
		}
		return auth, nil
	}

	// HTTPS auth
	if c.Password == "" {
		return nil, apperrors.ErrHTTPSPasswordRequired
	}

	return &http.BasicAuth{
		Username: "oauth2",
		Password: c.Password,
	}, nil
}

// TestConnection tests the connection to the remote repository.
func (c *RemoteConfig) TestConnection(ctx context.Context) error {
	if !c.IsEnabled() {
		return apperrors.ErrRemoteNotConfigured
	}

	auth, err := c.GetAuth()
	if err != nil {
		return fmt.Errorf("get auth: %w", err)
	}

	// Try to list remote references to verify connectivity
	rem := git.NewRemote(nil, &config.RemoteConfig{
		Name: "origin",
		URLs: []string{c.URL},
	})

	_, err = rem.ListContext(ctx, &git.ListOptions{
		Auth: auth,
	})
	if err != nil {
		// Empty repository is a valid connection
		if err.Error() == "remote repository is empty" {
			return nil
		}
		return fmt.Errorf("list remote: %w", err)
	}

	return nil
}
