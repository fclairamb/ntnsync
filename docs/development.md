# Development Guidelines

## Prerequisites

- Go 1.25+
- Docker (for container builds)

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make build` | Compile binary with version info |
| `make test` | Run tests |
| `make clean` | Remove binary |
| `make tidy` | Run `go mod tidy` |
| `make docker-test` | Build and test Docker image |
| `make intercept` | Set up Telepresence intercept for dev |

## Logging

### Log Format Configuration

Set `NTN_LOG_FORMAT` environment variable to control output:
- `text` (default): Human-readable format for development
- `json`: Structured JSON for CI/CD and log aggregation

```bash
# JSON format for production/CI
NTN_LOG_FORMAT=json ./ntnsync sync

# Text format (default)
./ntnsync sync -v
```

### Using slog

Always use the logger with context when available:

```go
// Good
c.logger.DebugContext(ctx, "processing page", "page_id", pageID)
c.logger.InfoContext(ctx, "sync complete", "pages", count)
slog.DebugContext(ctx, "operation started")

// Avoid (when context is available)
c.logger.Debug("processing page", "page_id", pageID)
slog.Debug("operation started")
```

Using context-aware logging enables better tracing and correlation of log entries.

## Code Organization

**Main packages**:
- `internal/cmd/` - CLI command handlers
- `internal/notion/` - Notion API client and types
- `internal/sync/` - Sync logic (crawler, converter, queue, state)
- `internal/store/` - Storage abstraction (git-backed filesystem)
- `internal/webhook/` - Webhook server for real-time sync
- `internal/version/` - Version information

## Testing

```bash
go test ./...
```

## Building

```bash
go build -o ntnsync .
```

With version information:

```bash
VERSION=$(git describe --tags --always)
COMMIT=$(git rev-parse --short HEAD)
GIT_TIME=$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)

go build -ldflags "-X 'github.com/fclairamb/ntnsync/internal/version.Version=$VERSION' \
                  -X 'github.com/fclairamb/ntnsync/internal/version.Commit=$COMMIT' \
                  -X 'github.com/fclairamb/ntnsync/internal/version.GitTime=$GIT_TIME'" \
  -o ntnsync .
```

## Docker

Build the Docker image:

```bash
docker build -t ntnsync .
```

Run with Docker:

```bash
docker run --rm \
  -e NOTION_TOKEN=secret_xxx \
  -v $(pwd)/notion:/data \
  ntnsync sync
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests: `go test ./...`
5. Submit a pull request

PR titles should follow [Conventional Commits](https://www.conventionalcommits.org/):
- `feat: add new feature`
- `fix: fix bug`
- `docs: update documentation`
- `chore: maintenance task`
