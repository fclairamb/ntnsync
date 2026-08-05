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
- **Database properties** — Database-entry properties are exported in the `properties` frontmatter
- **Depth control** — Limit block discovery depth for faster syncs
- **Clean git history** — Optional separate branch for high-frequency queue commits

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

## What the synced repository looks like

The target repository mirrors your Notion hierarchy. Each page becomes `<name>.md`, and its children live in a matching `<name>/` directory:

```
your-backup-repo/
├── root.md                          # Root pages manifest
├── tech/                            # One folder per root.md entry
│   ├── wiki.md                      # The root page itself
│   └── wiki/                        # Children mirror the Notion hierarchy
│       ├── architecture.md
│       ├── architecture/
│       │   ├── database-schema.md
│       │   └── files/               # Downloaded images and attachments
│       │       └── diagram.png
│       ├── releases.md              # A database page, listing its entries
│       └── releases/
│           ├── client-app-v1-2-0.md # Database entries are plain pages
│           └── client-app-v1-3-0.md
├── product/
│   └── specs.md
└── .notion-sync/                    # Sync metadata (committed too)
    ├── state.json                   # Global sync state
    ├── ids/                         # Page and file registries
    └── queue/                       # Pending sync work (see NTN_QUEUE_BRANCH)
```

File paths are derived from the page title at first sync and **never change afterwards**, even when pages are renamed in Notion — so links and git history stay stable.

### Frontmatter

Every markdown file starts with YAML frontmatter describing the page:

```yaml
---
ntnsync_version: 0.8.1
notion_id: 159aa28b-3ffb-808a-a6db-fdb9fa28c1d9
title: "Architecture"
notion_type: page
notion_folder: tech
file_path: tech/wiki/architecture.md
created_by: "Jane Doe <jane@example.com> [751deade]"
last_edited_by: "John Smith <john@example.com> [306d872b]"
last_edited: 2026-03-23T12:30:00Z
last_synced: 2026-06-09T18:16:13Z
icon: "emoji:📝"
notion_parent_id: 119aa28b3ffb8073b4f4e2f9ef243691
is_root: false
notion_url: https://www.notion.so/Architecture-159aa28b3ffb808aa6dbfdb9fa28c1d9
---
```

Pages that are **database entries** additionally carry their database properties, passed through under the `properties` key:

```yaml
properties:
  Status: "In Progress"
  Priority: "High"
  Version: "1.2.0"
  Tags:
    - "feature"
    - "urgent"
---
```

Select, multi-select, status, date, number, checkbox, people, and relation properties are flattened to their display values, so the exported markdown is self-contained and greppable.

### Separate queue branch

The `.notion-sync/queue/` directory changes on every pull and webhook event, which would flood the main branch history with "queued pages" commits. Setting `NTN_QUEUE_BRANCH=queue` routes queue commits to a dedicated branch (auto-created if missing), while page content, registries, and state stay on the main branch. The goal: the main branch history only contains meaningful content changes, making diffs and reviews of your Notion backup actually readable.

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
| `NTN_QUEUE_BRANCH` | | Commit `.notion-sync/queue/` to a separate branch (e.g. `queue`), keeping high-frequency queue commits out of the main history |
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
