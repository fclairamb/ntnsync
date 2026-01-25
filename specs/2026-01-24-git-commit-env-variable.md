# Git Commit via Environment Variable

**Date**: 2026-01-24
**Status**: Draft

## Overview

Replace the `--commit` CLI flag with a `NTN_COMMIT` environment variable to trigger git commits. This applies to both `add` and `sync` commands.

## Motivation

Current approach:
- The `--commit` flag must be explicitly passed on every command
- Inconsistent with other configuration options that use environment variables
- Not suitable for automated pipelines where environment configuration is preferred

With `NTN_COMMIT`:
- Consistent with existing `NTN_*` environment variable pattern (`NTN_DIR`, `NTN_GIT_URL`, etc.)
- Easier to configure in CI/CD pipelines and container deployments
- Single configuration point for commit behavior across all commands
- Works seamlessly with both local and remote git modes

## Design

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_COMMIT` | Enable automatic git commit after changes | `false` |
| `NTN_COMMIT_PERIOD` | Commit changes periodically during sync | `0` (disabled) |
| `NTN_PUSH` | Push to remote after commits | `true` when `NTN_GIT_URL` is set |

#### `NTN_COMMIT`

**Valid values**:
- `true`, `1`, `yes` - Enable commits
- `false`, `0`, `no`, (unset) - Disable commits (default)

#### `NTN_COMMIT_PERIOD`

**Valid values**:
- `0` or unset - Disabled (only commit at end of command)
- Duration string - Commit periodically (e.g., `30s`, `1m`, `5m`)

When `NTN_COMMIT_PERIOD` is set to a non-zero duration:
- A commit is triggered if the time since the last commit exceeds the period
- The check occurs after each queue file is written or deleted
- This ensures incremental progress is saved during long sync operations
- Requires `NTN_COMMIT=true` to have any effect

**Note**: Setting `NTN_COMMIT_PERIOD` implicitly enables `NTN_COMMIT` behavior. If `NTN_COMMIT_PERIOD` is set to a valid duration, commits are enabled regardless of `NTN_COMMIT` value.

#### `NTN_PUSH`

**Valid values**:
- `true`, `1`, `yes` - Push to remote after commits
- `false`, `0`, `no` - Commit locally only, do not push

**Default behavior**:
- When `NTN_GIT_URL` is set (remote mode): defaults to `true`
- When `NTN_GIT_URL` is not set (local mode): defaults to `false`

**Note**: When `NTN_GIT_URL` is not set, you can still set `NTN_PUSH=true` to push to the local repository's configured remote (e.g., set via `git remote add origin ...`).

This allows:
- Batching multiple syncs before a single push (`NTN_PUSH=false`)
- Testing changes locally before pushing
- Pushing to an existing git remote without using `NTN_GIT_URL` (`NTN_PUSH=true`)

### Affected Commands

#### `sync` command

Currently:
```bash
./ntnsync sync --commit              # Commits after sync
./ntnsync sync                       # No commit
```

After this change:
```bash
export NTN_COMMIT=true
./ntnsync sync                       # Commits after sync

# Or without export
NTN_COMMIT=true ./ntnsync sync   # Commits after sync
./ntnsync sync                       # No commit (NTN_COMMIT not set)
```

#### `add` command

Currently:
```bash
./ntnsync add <page_id> --folder tech   # No commit option
```

After this change:
```bash
export NTN_COMMIT=true
./ntnsync add <page_id> --folder tech   # Commits after adding page

# Or without export
NTN_COMMIT=true ./ntnsync add <page_id> --folder tech
```

### Interaction with Remote Git

| `NTN_COMMIT` | `NTN_COMMIT_PERIOD` | `NTN_GIT_URL` | `NTN_PUSH` | Behavior |
|--------------|---------------------|---------------|------------|----------|
| unset/false | unset/0 | unset | any | No commit, no push |
| unset/false | unset/0 | set | any | No commit, no push |
| true | unset/0 | unset | unset (default false) | Commit locally at end |
| true | unset/0 | unset | true | Commit and push to local repo's remote |
| true | unset/0 | set | unset (default true) | Commit and push at end |
| true | unset/0 | set | false | Commit locally only |
| any | `1m` | unset | unset (default false) | Commit locally every 1m + at end |
| any | `1m` | unset | true | Commit and push to local repo's remote every 1m |
| any | `1m` | set | unset (default true) | Commit and push every 1m + at end |
| any | `1m` | set | false | Commit locally every 1m + at end, no push |

### Removed Flags

The following CLI flags are removed:

| Command | Removed Flag | Replacement |
|---------|--------------|-------------|
| `sync` | `--commit` | `NTN_COMMIT=true` |
| `sync` | `--no-push` | `NTN_PUSH=false` |

## Implementation

### Parse Environment Variables

```go
func isCommitEnabled() bool {
    // NTN_COMMIT_PERIOD implicitly enables commits
    if getCommitPeriod() > 0 {
        return true
    }
    val := strings.ToLower(os.Getenv("NTN_COMMIT"))
    return val == "true" || val == "1" || val == "yes"
}

func getCommitPeriod() time.Duration {
    val := os.Getenv("NTN_COMMIT_PERIOD")
    if val == "" || val == "0" {
        return 0
    }
    d, err := time.ParseDuration(val)
    if err != nil {
        return 0
    }
    return d
}

func isPushEnabled() bool {
    val := os.Getenv("NTN_PUSH")
    if val == "" {
        // Default to true when NTN_GIT_URL is set (remote mode)
        // Default to false otherwise (local mode, but user can enable to push to existing remote)
        return os.Getenv("NTN_GIT_URL") != ""
    }
    val = strings.ToLower(val)
    return val == "true" || val == "1" || val == "yes"
}
```

### Periodic Commit Tracker

```go
type CommitTracker struct {
    lastCommit time.Time
    period     time.Duration
    mu         sync.Mutex
}

func NewCommitTracker() *CommitTracker {
    return &CommitTracker{
        lastCommit: time.Now(),
        period:     getCommitPeriod(),
    }
}

// ShouldCommit returns true if enough time has passed since last commit
func (t *CommitTracker) ShouldCommit() bool {
    if t.period == 0 {
        return false
    }
    t.mu.Lock()
    defer t.mu.Unlock()
    return time.Since(t.lastCommit) >= t.period
}

// MarkCommitted records that a commit was just made
func (t *CommitTracker) MarkCommitted() {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.lastCommit = time.Now()
}
```

### Command Changes

#### `sync` command

```go
func (c *Client) Sync(ctx context.Context) error {
    tracker := NewCommitTracker()

    // Process each queue item
    for _, item := range queue {
        if err := c.processQueueItem(ctx, item); err != nil {
            return err
        }

        // Check for periodic commit after each queue file is processed
        if tracker.ShouldCommit() {
            if err := c.commitAndPush(ctx, "periodic sync"); err != nil {
                return err
            }
            tracker.MarkCommitted()
        }
    }

    // Final commit at end (if NTN_COMMIT is enabled and there are uncommitted changes)
    if isCommitEnabled() {
        if err := c.commitAndPush(ctx, "sync complete"); err != nil {
            return err
        }
    }

    return nil
}

func (c *Client) commitAndPush(ctx context.Context, msg string) error {
    if err := c.git.Commit(ctx, msg); err != nil {
        return err
    }
    // Push if enabled (NTN_GIT_URL set and NTN_PUSH not disabled)
    if isPushEnabled() {
        if err := c.git.Push(ctx); err != nil {
            return err
        }
    }
    return nil
}
```

#### `add` command

```go
func (c *Client) Add(ctx context.Context, pageID, folder string) error {
    // ... existing add logic ...

    // Commit if NTN_COMMIT is enabled
    if isCommitEnabled() {
        if err := c.commitAndPush(ctx, "add page"); err != nil {
            return fmt.Errorf("commit failed: %w", err)
        }
    }

    return nil
}
```

## Examples

### Scenario 1: Local development with manual commits

```bash
# No NTN_COMMIT set - changes are not committed
./ntnsync add <page_id> --folder tech
./ntnsync sync

# User commits manually when ready
git add -A && git commit -m "Update docs"
```

### Scenario 2: Local development with auto-commit

```bash
export NTN_COMMIT=true

# Each command creates a commit
./ntnsync add <page_id> --folder tech    # Creates commit
./ntnsync sync                            # Creates commit if changes
```

### Scenario 3: CI/CD pipeline with remote push

```bash
export NTN_COMMIT=true
export NTN_GIT_URL=https://github.com/user/docs.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx

# Sync and push to remote (NTN_PUSH defaults to true when NTN_GIT_URL is set)
./ntnsync pull --since 2h
./ntnsync sync   # Commits and pushes automatically
```

### Scenario 3b: Commit without push (batch multiple syncs)

```bash
export NTN_COMMIT=true
export NTN_GIT_URL=https://github.com/user/docs.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx
export NTN_PUSH=false

# Sync multiple folders, commit each but don't push yet
./ntnsync sync --folder tech      # Commits locally
./ntnsync sync --folder product   # Commits locally
./ntnsync sync --folder design    # Commits locally

# Push all commits at once
git push
```

### Scenario 4: Long sync with periodic commits

```bash
export NTN_COMMIT_PERIOD=1m
export NTN_GIT_URL=https://github.com/user/docs.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx

# Sync 500 pages - commits every minute during sync
./ntnsync sync

# Output:
# Processing queue: 500 pages
# [00:00:45] Synced 50 pages
# [00:01:02] Periodic commit: 52 files
# [00:01:03] Pushed to origin/main
# [00:01:45] Synced 100 pages
# [00:02:01] Periodic commit: 48 files
# [00:02:02] Pushed to origin/main
# ...
# [00:08:30] Synced 500 pages
# [00:08:31] Final commit: 35 files
# [00:08:32] Pushed to origin/main
# Done: 500 pages synced, 11 commits
```

### Scenario 5: GitHub Actions workflow

```yaml
name: Notion Sync
on:
  schedule:
    - cron: '0 * * * *'

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Sync Notion pages
        env:
          NOTION_TOKEN: ${{ secrets.NOTION_TOKEN }}
          NTN_COMMIT: true
          NTN_GIT_URL: https://github.com/${{ github.repository }}.git
          NTN_GIT_PASS: ${{ secrets.GITHUB_TOKEN }}
        run: |
          ./ntnsync pull --since 2h
          ./ntnsync sync
```

## Migration Guide

### For CLI users

Before:
```bash
./ntnsync sync --commit
./ntnsync sync --commit --no-push
```

After:
```bash
# Commit and push (NTN_PUSH defaults to true when NTN_GIT_URL is set)
NTN_COMMIT=true ./ntnsync sync

# Commit locally only (no push)
NTN_COMMIT=true NTN_PUSH=false ./ntnsync sync
```

### For scripts and CI/CD

Before:
```bash
./ntnsync sync --commit
```

After:
```bash
export NTN_COMMIT=true
./ntnsync sync
```

## Success Criteria

- [ ] `--commit` flag removed from `sync` command
- [ ] `--no-push` flag removed from `sync` command
- [ ] `NTN_COMMIT=true` triggers commit on `sync` command
- [ ] `NTN_COMMIT=true` triggers commit on `add` command
- [ ] `NTN_COMMIT_PERIOD` triggers periodic commits during sync
- [ ] `NTN_COMMIT_PERIOD` implicitly enables commit behavior
- [ ] Periodic commit check occurs after each queue file write/delete
- [ ] `NTN_PUSH` defaults to `true` when `NTN_GIT_URL` is set
- [ ] `NTN_PUSH` defaults to `false` when `NTN_GIT_URL` is not set
- [ ] `NTN_PUSH=true` pushes to local repo's remote when `NTN_GIT_URL` is not set
- [ ] `NTN_PUSH=false` prevents pushing even when `NTN_GIT_URL` is set
- [ ] Commit message format unchanged
- [ ] Documentation updated (CLAUDE.md, docs/cli-commands.md)
- [ ] Clear error messages for invalid environment variable values
