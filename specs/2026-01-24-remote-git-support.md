# Remote Git Support

**Date**: 2026-01-24
**Status**: Draft

## Overview

Add support for saving synced Notion pages to a remote git repository instead of the local filesystem. This enables using ntnsync as a headless service that pushes content directly to a remote repository without requiring local storage of the synchronized files.

## Motivation

Current workflow requires:
1. Running ntnsync locally
2. Syncing pages to local filesystem
3. Manually committing and pushing changes

With remote git support:
- Run ntnsync on a server/CI without persistent local storage
- Push Notion content directly to a remote repository
- Enable automated sync pipelines (cron jobs, webhooks)
- Reduce storage requirements on the sync machine
- Enable multi-destination sync (same content to multiple repos)

## Design

### Configuration via Environment Variables

Remote git configuration is done **exclusively via environment variables**. This is necessary because:
- The `.notion-sync/` directory is committed to the remote repository
- Storing remote credentials inside the repo it grants access to creates a security issue
- Environment variables are the standard approach for CI/CD and container deployments

**Required environment variables**:

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_DIR` | Local directory for the git repository | `./notion` |
| `NTN_GIT_URL` | Remote git repository URL | (none - local only) |
| `NTN_GIT_PASS` | Password or token for git authentication | (none) |

**Optional environment variables**:

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_GIT_BRANCH` | Target branch | `main` |
| `NTN_GIT_USER` | Git username for commits | `ntnsync` |
| `NTN_GIT_EMAIL` | Git email for commits | `ntnsync@local` |

**Authentication**:

When `NTN_GIT_URL` is set:
- For HTTPS URLs: Uses `NTN_GIT_PASS` as the password/token
- For SSH URLs: Uses system SSH agent or default key (`~/.ssh/id_rsa`, `~/.ssh/id_ed25519`)

### Directory Structure

```
# NTN_DIR (local git repository, defaults to ./notion)
./notion/
├── .git/                      # Git repository
├── .notion-sync/              # All state files (pushed to remote)
│   ├── state.json             # Folder list, pull tracking
│   ├── queue/                 # Queue files
│   │   ├── 00000001.json
│   │   └── 00000002.json
│   └── ids/
│       └── page-{id}.json     # Page registries
├── tech/
│   └── architecture.md
└── product/
    └── roadmap.md
```

**Everything in `NTN_DIR` is pushed to remote**, including:
- Queue files - enables resuming sync from another machine
- State files - folder list, pull tracking
- Page registries - page metadata
- Markdown content - the actual synced pages

### Command: `remote`

Test remote connection (configuration is via environment variables).

**Subcommands**:

```bash
# Show current configuration from environment
./ntnsync remote show

# Test remote connection
./ntnsync remote test
```

**Examples**:

```bash
# Configure via environment
export NTN_DIR=./notion
export NTN_GIT_URL=https://github.com/user/repo.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx

# Show configuration
./ntnsync remote show
# Output:
# Directory: ./notion
# Remote URL: https://github.com/user/repo.git
# Branch: main (default)
# Auth: token (NTN_GIT_PASS is set)

# Test the connection
./ntnsync remote test
# Output:
# Testing connection to https://github.com/user/repo.git...
# ✓ Authentication successful
# ✓ Branch 'main' exists
# ✓ Push access confirmed
```

### Modified Commands

#### `sync` command changes

When `NTN_GIT_URL` is set, sync automatically pushes to remote:

```bash
# Local-only mode (NTN_GIT_URL not set)
./ntnsync sync

# Remote mode (NTN_GIT_URL is set)
export NTN_GIT_URL=https://github.com/user/repo.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx
./ntnsync sync                    # Syncs and pushes to remote
./ntnsync sync --folder tech      # Sync specific folder
```

**Remote sync workflow**:
1. Pull latest from remote (if `NTN_DIR` exists and has remote)
2. Process queue and write files to `NTN_DIR`
3. Commit changes with standard message format
4. Push to remote
5. Update local state

**Commit message format**:
```
[ntnsync] Sync {count} pages

Pages updated:
- {folder}/{filename} (updated)
- {folder}/{filename} (created)
- {folder}/{filename} (deleted)

Synced at: {timestamp}
```

#### `add` command changes

When `NTN_GIT_URL` is set, add downloads to the local git directory:

```bash
# Set up remote
export NTN_DIR=./notion
export NTN_GIT_URL=https://github.com/user/repo.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx

# Add downloads to NTN_DIR, queues for sync
./ntnsync add <page_id> --folder tech
# Creates: ./notion/tech/page-title.md
```

### State Synchronization

All state is stored inside `NTN_DIR` and pushed to remote:

- `NTN_DIR/.notion-sync/state.json` - Folder list and pull tracking
- `NTN_DIR/.notion-sync/queue/` - Pending pages to sync
- `NTN_DIR/.notion-sync/ids/page-{id}.json` - Page registries

This enables:
- Multiple machines to sync to the same repo
- Resume sync from another machine (queue is preserved)
- State recovery by cloning the remote repo
- Consistent state between local and remote

### Conflict Resolution

**Strategy**: Last-write-wins with backup

When remote has changes not in local:
1. Fetch remote changes
2. If conflict on same file:
   - Keep remote version as `{filename}.remote-backup`
   - Apply local changes
   - Log warning about conflict
3. Push all changes

**Conflict detection**:
```bash
./ntnsync sync
# Output:
# Pulling from remote...
# ⚠ Conflict detected: tech/architecture.md
#   Created backup: tech/architecture.remote-backup.md
# Pushing 3 files...
# Done: 3 files pushed, 1 conflict (backup created)
```

## Implementation

### Git Operations

Use `go-git` library for pure Go git operations:

```go
import "github.com/go-git/go-git/v5"

// Clone repository
repo, err := git.PlainClone(tmpDir, false, &git.CloneOptions{
    URL:           remoteURL,
    ReferenceName: plumbing.NewBranchReferenceName(branch),
    Auth:          auth,
    SingleBranch:  true,
    Depth:         1,
})

// Add files
worktree, _ := repo.Worktree()
worktree.Add("tech/architecture.md")

// Commit
commit, _ := worktree.Commit("[ntnsync] Sync 3 pages", &git.CommitOptions{
    Author: &object.Signature{
        Name:  "ntnsync",
        Email: "ntnsync@local",
        When:  time.Now(),
    },
})

// Push
repo.Push(&git.PushOptions{Auth: auth})
```

### Authentication Implementation

```go
func getAuth(gitURL string) (transport.AuthMethod, error) {
    password := os.Getenv("NTN_GIT_PASS")

    // Determine if SSH or HTTPS based on URL
    if strings.HasPrefix(gitURL, "git@") || strings.HasPrefix(gitURL, "ssh://") {
        // SSH authentication - use system SSH agent or default keys
        return ssh.NewSSHAgentAuth("git")
    }

    // HTTPS authentication - use NTN_GIT_PASS as token
    if password == "" {
        return nil, fmt.Errorf("NTN_GIT_PASS environment variable required for HTTPS URLs")
    }

    return &http.BasicAuth{
        Username: "oauth2",  // Standard for token auth
        Password: password,
    }, nil
}
```

### Sync Flow

```go
func (c *Client) Sync(ctx context.Context, folder string) error {
    ntnDir := os.Getenv("NTN_DIR")
    if ntnDir == "" {
        ntnDir = "./notion"
    }

    gitURL := os.Getenv("NTN_GIT_URL")

    // 1. If remote configured, pull latest
    if gitURL != "" {
        if err := c.pullRemote(ctx, ntnDir); err != nil {
            return fmt.Errorf("pull failed: %w", err)
        }
    }

    // 2. Process queue (write to NTN_DIR)
    changes, err := c.processQueueToDir(ctx, ntnDir, folder)
    if err != nil {
        return err
    }

    // 3. Commit and push if remote configured
    if gitURL != "" && len(changes) > 0 {
        if err := c.commitAndPush(ctx, ntnDir, changes); err != nil {
            return fmt.Errorf("push failed: %w", err)
        }
    }

    c.logger.InfoContext(ctx, "sync complete",
        "files_changed", len(changes),
        "folder", folder,
        "remote", gitURL != "")

    return nil
}
```

## Examples

### Scenario 1: Basic remote setup

```bash
# Configure via environment variables
export NTN_DIR=./notion
export NTN_GIT_URL=https://github.com/user/notion-docs.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx
export NOTION_TOKEN=secret_xxxxxxxxxxxx

# Add a page
./ntnsync add abc123 --folder tech

# Sync (automatically pushes to remote when NTN_GIT_URL is set)
./ntnsync sync

# Output:
# Pulling from https://github.com/user/notion-docs.git...
# Processing queue: 1 page
# Downloaded: tech/architecture.md
# Committing changes...
# Pushing to origin/main...
# Done: 1 file pushed
```

### Scenario 2: CI/CD pipeline

```yaml
# .github/workflows/notion-sync.yml
name: Notion Sync
on:
  schedule:
    - cron: '0 * * * *'  # Every hour
  workflow_dispatch:

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - name: Setup ntnsync
        run: |
          curl -sL https://github.com/user/ntnsync/releases/latest/download/ntnsync-linux-amd64 -o ntnsync
          chmod +x ntnsync

      - name: Sync Notion pages
        env:
          NOTION_TOKEN: ${{ secrets.NOTION_TOKEN }}
          NTN_DIR: ./notion
          NTN_GIT_URL: https://github.com/${{ github.repository }}.git
          NTN_GIT_PASS: ${{ secrets.GITHUB_TOKEN }}
        run: |
          ./ntnsync pull --since 2h
          ./ntnsync sync
```

### Scenario 3: SSH authentication

```bash
# For SSH URLs, uses system SSH agent or default keys
export NTN_DIR=./notion
export NTN_GIT_URL=git@github.com:user/docs.git
# NTN_GIT_PASS not needed for SSH

# Ensure SSH key is loaded
ssh-add ~/.ssh/id_ed25519

# Sync
./ntnsync sync
```

### Scenario 4: Local-only mode (no remote)

```bash
# Without NTN_GIT_URL, works in local-only mode
export NTN_DIR=./my-notion-docs
# NTN_GIT_URL not set

# Add and sync locally
./ntnsync add page1 --folder tech
./ntnsync sync

# Files are written to ./my-notion-docs but not pushed anywhere
```

## Testing

### Test Repository

Use test repository for development and CI:
- Repository: `git@github.com:fclairamb/res-notion-test.git`
- Supports both SSH and HTTPS authentication

### Test Scenarios

1. **Authentication**
   - SSH key authentication
   - Personal access token (HTTPS)
   - Invalid credentials (should fail gracefully)
   - Missing environment variable

2. **Basic Operations**
   - Clone empty repository
   - Clone repository with existing content
   - Add single file
   - Add multiple files
   - Update existing file
   - Delete file

3. **Conflict Handling**
   - Remote has changes not in local
   - Same file modified locally and remotely
   - File deleted remotely, modified locally

4. **Remote-Only Mode**
   - Files not stored locally
   - State files preserved locally
   - Correct content pushed to remote

5. **Error Recovery**
   - Network failure during push
   - Push rejected (non-fast-forward)
   - Repository not found
   - Permission denied

6. **Integration**
   - `add` + `sync` workflow with remote
   - `pull` + `sync` workflow with remote
   - Large number of files (100+)
   - Deep folder hierarchy

### Test Commands

```bash
# Unit tests
go test ./internal/git/...

# Integration tests (requires network)
NTN_DIR=/tmp/notion-test \
NTN_GIT_URL=https://github.com/fclairamb/res-notion-test.git \
NTN_GIT_PASS=ghp_xxxxxxxxxxxx \
go test ./internal/git/... -tags=integration

# Manual testing
export NTN_DIR=/tmp/notion-test
export NTN_GIT_URL=https://github.com/fclairamb/res-notion-test.git
export NTN_GIT_PASS=ghp_xxxxxxxxxxxx

./ntnsync remote test
./ntnsync add <test-page-id> --folder test
./ntnsync sync
```

## Security Considerations

1. **Credentials via environment variables only** - Never in files
2. **SSH keys should have passphrase** - Or use ssh-agent
3. **Limit token scope** - Use minimal permissions (repo write only)
4. **Audit remote access** - Log all push operations
5. **Validate remote URL** - Prevent SSRF attacks
6. **Sanitize commit messages** - Escape special characters from page titles

## Success Criteria

- [ ] `NTN_DIR` configures local git directory (defaults to `./notion`)
- [ ] `NTN_GIT_URL` enables remote git push
- [ ] `NTN_GIT_PASS` authenticates HTTPS connections
- [ ] `remote show` displays current configuration
- [ ] `remote test` validates connection and permissions
- [ ] `sync` automatically pushes when `NTN_GIT_URL` is set
- [ ] SSH authentication works (via ssh-agent)
- [ ] Token (HTTPS) authentication works
- [ ] All state files pushed to remote (including queue)
- [ ] Conflicts handled gracefully with backups
- [ ] State synchronization works across machines
- [ ] Error messages are clear and actionable
- [ ] Works in CI/CD environment (GitHub Actions)

## Future Enhancements

1. **Multiple remotes** - Support multiple `NTN_GIT_URL_*` variables
2. **Clone/init command** - Initialize `NTN_DIR` from existing remote
3. **Webhook triggers** - Push on Notion change
4. **Signed commits** - GPG signing for audit trails
5. **Branch per folder** - `NTN_GIT_BRANCH_<folder>` overrides
