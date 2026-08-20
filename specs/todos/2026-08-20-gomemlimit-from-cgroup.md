---
model: sonnet
effort: low
---

# Set GOMEMLIMIT from the cgroup memory limit at startup

**Date**: 2026-08-20
**Parent**: `specs/2026-08-19-oom-large-repo.md` (Tier 1, in-repo portion)

## Problem

Go's GC has no knowledge of the container memory limit. On the default `GOGC=100`
schedule it lets the heap grow toward twice the live set and gets OOM-killed by the
kernel before it ever collects. This was a contributing cause of the `serve` OOM kills
(`Reason: OOMKilled`, `Exit Code: 137`).

Setting `GOMEMLIMIT` fixes this, but relying on the deployment to set it means every
deployment has to remember. The binary can discover it itself.

## Proposal

At process startup, if `GOMEMLIMIT` is **not** already set in the environment, read the
cgroup memory limit and apply 85% of it via `runtime/debug.SetMemoryLimit`.

- cgroup v2: `/sys/fs/cgroup/memory.max`
- cgroup v1 fallback: `/sys/fs/cgroup/memory/memory.limit_in_bytes`

Rules:

- If `GOMEMLIMIT` is already set in the environment, do nothing — the Go runtime has
  already applied it and an explicit operator setting must win.
- If the file is missing, unreadable, or contains `max` (cgroup v2 for "unlimited"), do
  nothing.
- cgroup v1 reports a sentinel near `math.MaxInt64` when unlimited — treat any value
  above a sane ceiling (say 1 TiB) as unlimited and do nothing.
- On success log at INFO: the detected limit and the applied limit.

Put the helper in its own small package or file (e.g. `internal/cmd/memlimit.go`) and call
it from `run()` in `main.go` before `cmd.NewApp()`, or at the top of `NewApp()`. It must be
a no-op on macOS/Windows — the files simply won't exist, which the "unreadable → do
nothing" rule already covers.

## Acceptance criteria

- `GOMEMLIMIT` unset + a readable cgroup v2 `memory.max` of `3221225472` → memory limit set
  to 85% of that (2737418240 bytes), logged.
- `GOMEMLIMIT` set in env → helper makes no call to `SetMemoryLimit`.
- `memory.max` containing `max` → no call.
- Missing files → no call, no error, no warning spam.
- Table-driven unit tests for the parse/decide helper, with the file path injected so the
  tests don't depend on the host's real cgroup files.
- `go build ./...`, `golangci-lint run ./...` and `go test ./...` all pass.
