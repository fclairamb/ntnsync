# Dockerfile Support

**Date**: 2026-01-24
**Status**: Draft

## Overview

Add a Dockerfile to containerize ntnsync for deployment in container environments. This enables running ntnsync as a scheduled service in Kubernetes, Docker Compose, or any container orchestration platform.

## Motivation

Current deployment requires:
1. Installing Go and building from source
2. Or downloading a pre-built binary for the target architecture
3. Setting up environment variables and scheduling manually

With Docker support:
- Consistent deployment across all environments
- Easy integration with Kubernetes CronJobs
- Simplified CI/CD pipelines
- Pre-configured environment handling
- Multi-architecture support via Docker buildx

## Design

### Dockerfile Structure

Following the multi-stage build pattern from dbbat:

```dockerfile
# =============================================================================
# ntnsync Multi-Stage Build
# =============================================================================
# This Dockerfile builds a minimal container for ntnsync:
#   1. Build: Go binary compiled with CGO disabled
#   2. Runtime: Minimal distroless image
#
# Usage:
#   docker build -t ntnsync .
#   docker run -e NOTION_TOKEN=xxx ntnsync sync
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build
# -----------------------------------------------------------------------------
FROM golang:1.25-trixie AS builder

WORKDIR /app

# Install dependencies first (better layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build arguments for version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
    -X 'main.Version=${VERSION}' \
    -X 'main.Commit=${COMMIT}' \
    -X 'main.BuildTime=${BUILD_TIME}'" \
    -o ntnsync .

# -----------------------------------------------------------------------------
# Stage 2: Runtime
# -----------------------------------------------------------------------------
FROM gcr.io/distroless/base-debian13:nonroot

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/ntnsync .

# Default data directory
# Mount a volume here or use NTN_GIT_URL for remote storage
VOLUME /data

# Set default environment variables
ENV NTN_DIR=/data

# Run the binary (default to help)
ENTRYPOINT ["./ntnsync"]
CMD ["--help"]
```

### Environment Variables

The container relies on environment variables for configuration:

| Variable | Required | Description | Default |
|----------|----------|-------------|---------|
| `NOTION_TOKEN` | Yes | Notion integration token | - |
| `NTN_DIR` | No | Data directory inside container | `/data` |
| `NTN_GIT_URL` | No | Remote git repository URL | - |
| `NTN_GIT_PASS` | No | Git password/token | - |
| `NTN_COMMIT` | No | Enable auto-commit | `false` |
| `NTN_PUSH` | No | Enable auto-push | - |
| `NTN_STORAGE` | No | Storage mode (fs/db) | `fs` |

### Docker Compose

For local development and testing:

```yaml
# docker-compose.yml
services:
  ntnsync:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: ntnsync
    environment:
      NOTION_TOKEN: ${NOTION_TOKEN}
      NTN_DIR: /data
      # For remote git mode:
      # NTN_GIT_URL: https://github.com/user/repo.git
      # NTN_GIT_PASS: ${GIT_TOKEN}
    volumes:
      - notion-data:/data
    # Run sync command
    command: ["sync"]

volumes:
  notion-data:
```

### Kubernetes CronJob

Example deployment for scheduled syncing:

```yaml
# k8s/cronjob.yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: ntnsync
spec:
  schedule: "0 * * * *"  # Every hour
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: ntnsync
              image: ghcr.io/fclairamb/ntnsync:latest
              args: ["pull", "--since", "2h"]
              env:
                - name: NOTION_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: ntnsync-secrets
                      key: notion-token
                - name: NTN_GIT_URL
                  value: "https://github.com/user/repo.git"
                - name: NTN_GIT_PASS
                  valueFrom:
                    secretKeyRef:
                      name: ntnsync-secrets
                      key: git-token
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: ntnsync-sync
spec:
  schedule: "5 * * * *"  # 5 minutes after pull
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: ntnsync
              image: ghcr.io/fclairamb/ntnsync:latest
              args: ["sync"]
              env:
                - name: NOTION_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: ntnsync-secrets
                      key: notion-token
                - name: NTN_GIT_URL
                  value: "https://github.com/user/repo.git"
                - name: NTN_GIT_PASS
                  valueFrom:
                    secretKeyRef:
                      name: ntnsync-secrets
                      key: git-token
                - name: NTN_COMMIT
                  value: "true"
```

### Multi-Architecture Support

Build for multiple architectures using Docker buildx:

```bash
# Create builder
docker buildx create --name ntnsync-builder --use

# Build and push multi-arch image
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --tag ghcr.io/fclairamb/ntnsync:latest \
  --push .
```

### Makefile Targets

Add Docker-related targets to the Makefile:

```makefile
# Docker targets
DOCKER_IMAGE=ghcr.io/fclairamb/ntnsync
VERSION?=$(shell git describe --tags --always --dirty)
COMMIT?=$(shell git rev-parse --short HEAD)
BUILD_TIME?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

.PHONY: docker-build docker-push docker-run

docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):latest \
		.

docker-push: docker-build
	docker push $(DOCKER_IMAGE):$(VERSION)
	docker push $(DOCKER_IMAGE):latest

docker-run:
	docker run --rm -it \
		-e NOTION_TOKEN=$(NOTION_TOKEN) \
		-v $(PWD)/notion:/data \
		$(DOCKER_IMAGE):latest $(ARGS)
```

## Implementation

### Files to Create

1. **Dockerfile** - Multi-stage build as described above

2. **docker-compose.yml** - Development/testing compose file

3. **.dockerignore** - Exclude unnecessary files:
   ```
   .git
   .gitignore
   .github
   *.md
   !README.md
   Makefile
   notion/
   specs/
   docs/
   k8s/
   scripts/
   .notion-sync/
   ```

4. **k8s/cronjob.yaml** - Kubernetes CronJob example

5. **k8s/secret.yaml.example** - Secret template:
   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: ntnsync-secrets
   type: Opaque
   stringData:
     notion-token: "secret_xxx"
     git-token: "ghp_xxx"
   ```

### Version Information

Add version variables to main.go if not already present:

```go
var (
    Version   = "dev"
    Commit    = "unknown"
    BuildTime = "unknown"
)
```

Expose via a `version` command:
```bash
./ntnsync version
# ntnsync version dev (commit: unknown, built: unknown)
```

## Usage Examples

### Local Build and Run

```bash
# Build the image
docker build -t ntnsync .

# Run sync with local volume
docker run --rm \
  -e NOTION_TOKEN=secret_xxx \
  -v $(pwd)/notion:/data \
  ntnsync sync

# Run with remote git
docker run --rm \
  -e NOTION_TOKEN=secret_xxx \
  -e NTN_GIT_URL=https://github.com/user/repo.git \
  -e NTN_GIT_PASS=ghp_xxx \
  ntnsync sync
```

### Docker Compose

```bash
# Start sync
docker compose run ntnsync sync

# Pull changes
docker compose run ntnsync pull --since 24h

# List pages
docker compose run ntnsync list --tree
```

### CI/CD with GitHub Actions

```yaml
# .github/workflows/docker.yml
name: Docker Build

on:
  push:
    tags:
      - 'v*'

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v5
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: |
            ghcr.io/fclairamb/ntnsync:${{ github.ref_name }}
            ghcr.io/fclairamb/ntnsync:latest
          build-args: |
            VERSION=${{ github.ref_name }}
            COMMIT=${{ github.sha }}
            BUILD_TIME=${{ github.event.head_commit.timestamp }}
```

## Testing

### Build Testing

```bash
# Build the image
docker build -t ntnsync:test .

# Verify binary works
docker run --rm ntnsync:test --help

# Check image size
docker images ntnsync:test --format "{{.Size}}"
# Expected: ~15-25MB (distroless base + Go binary)
```

### Integration Testing

```bash
# Test with mock notion data
export NOTION_TOKEN=test_token
docker run --rm \
  -e NOTION_TOKEN=$NOTION_TOKEN \
  ntnsync:test list

# Test volume mounting
docker run --rm \
  -e NOTION_TOKEN=$NOTION_TOKEN \
  -v $(pwd)/test-data:/data \
  ntnsync:test sync
```

### Multi-Architecture Testing

```bash
# Build for specific platform
docker buildx build --platform linux/arm64 -t ntnsync:arm64-test .

# Run with platform emulation
docker run --platform linux/arm64 --rm ntnsync:arm64-test --help
```

## Security Considerations

1. **Distroless image** - No shell or package manager reduces attack surface
2. **Non-root user** - Container runs as nonroot (uid 65532)
3. **No secrets in image** - All sensitive data via environment variables
4. **Read-only filesystem** - Possible with volume for `/data`
5. **Network policy** - Only needs outbound to Notion API and git remote

## Success Criteria

- [ ] Dockerfile builds successfully
- [ ] Image size < 30MB
- [ ] Runs as non-root user
- [ ] All commands work in container (`add`, `pull`, `sync`, `list`)
- [ ] Volume mounting works for persistent data
- [ ] Remote git mode works in container
- [ ] Environment variables properly configure the application
- [ ] Multi-architecture builds work (amd64, arm64)
- [ ] Docker Compose example works
- [ ] Kubernetes CronJob example works

## Future Enhancements

1. **Health endpoint** - Add `/health` for liveness probes
2. **Metrics endpoint** - Prometheus metrics for monitoring
3. **Webhook server mode** - Long-running container with webhook listener
4. **Init container** - For one-time setup tasks
5. **Helm chart** - Package Kubernetes resources
