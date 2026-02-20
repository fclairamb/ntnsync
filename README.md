# ntnsync

Sync Notion pages to a git repository as markdown files.

[![CI](https://github.com/fclairamb/ntnsync/actions/workflows/ci.yml/badge.svg)](https://github.com/fclairamb/ntnsync/actions/workflows/ci.yml)
[![Release](https://github.com/fclairamb/ntnsync/actions/workflows/release.yml/badge.svg)](https://github.com/fclairamb/ntnsync/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/fclairamb/ntnsync)](https://goreportcard.com/report/github.com/fclairamb/ntnsync)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Features

- **Notion to Markdown** — Converts pages and databases to clean markdown with YAML frontmatter
- **Git integration** — Automatic commits and push to remote repositories
- **Stable file paths** — Paths never change when pages are renamed in Notion
- **Incremental sync** — Only processes pages that changed since the last pull
- **Webhook server** — Real-time sync via Notion webhook events
- **Folder organization** — Group pages into named folders (e.g., `tech`, `product`)
- **Database support** — Databases are synced as pages with child page listings
- **Depth control** — Limit block discovery depth for faster syncs

## Installation

### From source

```bash
go install github.com/fclairamb/ntnsync@latest
```

### Docker

```bash
docker pull ghcr.io/fclairamb/ntnsync:latest
```

### GitHub Releases

Download pre-built binaries for Linux, macOS, and Windows from the [releases page](https://github.com/fclairamb/ntnsync/releases).

## Quick start

### 1. Get a Notion API token

Create an integration at [notion.so/my-integrations](https://www.notion.so/my-integrations) and share your pages with it.

### 2. Set the token

```bash
export NOTION_TOKEN=secret_xxx
```

### 3. Configure root pages

Create `notion/root.md` with your root pages:

```markdown
# Root Pages

- [x] **tech**: https://www.notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d
- [x] **product**: https://www.notion.so/Specs-abc123def456
```

Each entry maps a Notion page (and all its children) to a local folder. Uncheck `[ ]` to disable a root without removing it.

### 4. Pull and sync

```bash
# Queue changed pages (use --since for the first pull)
ntnsync pull --since 30d

# Process the queue and write markdown files
NTN_COMMIT=true ntnsync sync
```

### 5. Keep it updated

```bash
# Pull recent changes and sync
ntnsync pull
NTN_COMMIT=true ntnsync sync
```

## Webhook server

For real-time sync, run `ntnsync serve` to receive Notion webhook events:

```bash
ntnsync serve --verbose
```

The server listens on port 8080 and exposes:
- `POST /webhooks/notion` — Receives Notion events, queues changed pages, and auto-syncs
- `GET /health` — Health check endpoint
- `GET /version` — Version info

Configure your [Notion integration](https://www.notion.so/my-integrations) to send webhooks to your server's URL.

## Kubernetes deployment

ntnsync runs well as a long-lived deployment with the webhook server. Here's a minimal setup:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ntnsync
  labels:
    app.kubernetes.io/name: ntnsync
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ntnsync
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ntnsync
    spec:
      containers:
      - name: ntnsync
        image: ghcr.io/fclairamb/ntnsync:0.6.3
        args: ["serve", "--verbose"]
        ports:
        - containerPort: 8080
        env:
        - name: NTN_GIT_URL
          value: "https://github.com/your-org/your-repo.git"
        - name: NTN_DIR
          value: "/tmp/data"
        - name: NTN_COMMIT
          value: "true"
        - name: NTN_COMMIT_PERIOD
          value: "1m"
        - name: NTN_LOG_FORMAT
          value: "json"
        - name: NTN_GIT_PASS
          valueFrom:
            secretKeyRef:
              name: ntnsync
              key: NTN_GIT_PASS
        - name: NOTION_TOKEN
          valueFrom:
            secretKeyRef:
              name: ntnsync
              key: NOTION_TOKEN
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          periodSeconds: 10
        startupProbe:
          httpGet:
            path: /health
            port: 8080
          failureThreshold: 180
          periodSeconds: 10
        resources:
          limits:
            cpu: "3"
            memory: 512Mi
          requests:
            memory: 20Mi
```

With `NTN_GIT_URL` set, ntnsync clones the repo to a temp directory and pushes changes back — no persistent volume needed.

See [deployment docs](website/docs/deployment.md) for the full setup including Service, Ingress, and Notion webhook configuration.

## Environment variables

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `NOTION_TOKEN` | | Notion API token (required) |
| `NTN_DIR` | `notion` | Storage directory path |

### Git

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_COMMIT` | `false` | Enable automatic git commits |
| `NTN_COMMIT_PERIOD` | | Commit periodically during sync (e.g., `1m`) |
| `NTN_PUSH` | auto | Push to remote after commits |
| `NTN_GIT_URL` | | Remote git repository URL |
| `NTN_GIT_PASS` | | Git password/token for authentication |
| `NTN_GIT_BRANCH` | `main` | Git branch name |
| `NTN_GIT_USER` | `ntnsync` | Git commit author name |
| `NTN_GIT_EMAIL` | `ntnsync@localhost` | Git commit author email |

### Performance

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_BLOCK_DEPTH` | `0` | Max block discovery depth (0 = unlimited) |
| `NTN_QUEUE_DELAY` | `0` | Delay between queue file processing |
| `NTN_MAX_FILE_SIZE` | `5MB` | Max file size to download |

### Webhook

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_WEBHOOK_PORT` | `8080` | HTTP port |
| `NTN_WEBHOOK_SECRET` | | HMAC secret for signature verification |
| `NTN_WEBHOOK_PATH` | `/webhooks/notion` | Webhook endpoint path |
| `NTN_WEBHOOK_AUTO_SYNC` | `true` | Auto-sync after receiving events |
| `NTN_WEBHOOK_SYNC_DELAY` | `0` | Debounce delay before processing |

### Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `NTN_LOG_FORMAT` | `text` | Log format: `text` or `json` |

## CLI commands

| Command | Description |
|---------|-------------|
| `pull` | Queue pages that changed since last pull |
| `sync` | Process the queue, download pages, write markdown |
| `list` | List folders and pages (`--tree` for hierarchy) |
| `status` | Show sync status and queue statistics |
| `get` | Fetch a single page by ID or URL |
| `scan` | Re-scan a page to discover children |
| `cleanup` | Delete orphaned pages not in root.md |
| `reindex` | Rebuild registries from markdown files |
| `remote` | Show or test remote git configuration |
| `serve` | Start webhook server for real-time sync |

See [CLI commands documentation](docs/cli-commands.md) for full details, flags, and examples.

## Documentation

| Document | Description |
|----------|-------------|
| [CLI Commands](docs/cli-commands.md) | All commands with flags and examples |
| [File Architecture](docs/file-architecture.md) | Directory structure, registries, queue system |
| [Markdown Conversion](docs/markdown-conversion.md) | How Notion blocks become markdown |
| [Development](docs/development.md) | Building, testing, contributing |
| [Commit Conventions](docs/commit-conventions.md) | Conventional commits guide |
| [Deployment](website/docs/deployment.md) | Kubernetes deployment guide |
| [Changelog](CHANGELOG.md) | Release history |

## License

[MIT](LICENSE)
