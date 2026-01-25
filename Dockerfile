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
ARG GIT_TIME=unknown

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
    -X 'github.com/fclairamb/ntnsync/internal/version.Version=${VERSION}' \
    -X 'github.com/fclairamb/ntnsync/internal/version.Commit=${COMMIT}' \
    -X 'github.com/fclairamb/ntnsync/internal/version.GitTime=${GIT_TIME}'" \
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
ENTRYPOINT ["./app/ntnsync"]
CMD ["--help"]
