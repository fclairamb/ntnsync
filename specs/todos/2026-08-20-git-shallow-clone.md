---
model: sonnet
effort: medium
---

# Shallow clone by default (`NTN_GIT_DEPTH`)

**Date**: 2026-08-20
**Parent**: `specs/2026-08-19-oom-large-repo.md` (Tier 2b)

## Problem

`LocalStore.cloneFromRemote()` in `internal/store/local.go` calls `git.PlainClone` with
`SingleBranch: true` but **no `Depth`**, so the initial clone fetches and parses the entire
history. On the reference repository that is 36,412 commits and a 1.1 GB packfile, and the
clone alone peaks at 1.32 GB RSS — over the container limit on its own.

Nothing in ntnsync reads history. The tool only ever writes new commits on top of the tip.

## Proposal

Add a `Depth` field to `RemoteConfig` (`internal/store/remote.go`), populated from
`NTN_GIT_DEPTH`, and pass it through to `git.CloneOptions.Depth` in `cloneFromRemote()`.

- **Default: 1** (shallow).
- `NTN_GIT_DEPTH=0` means full history — an explicit opt-out.
- Parse like the other int env vars in that file; a malformed value falls back to the
  default rather than failing startup.

Apply it to the queue store too: `createStore()` in `internal/cmd/app.go` builds a second
`RemoteConfig` for the queue branch by copying fields one by one — `Depth` must be copied
there as well, or the queue clone silently stays deep.

Measured effect: clone peak RSS 1.32 GB -> 705 MB.

## Risk to verify

Shallow clones were verified end-to-end with go-git v5.19.2 for
**clone -> add -> commit -> push** (push from a shallow clone works). The **pull** path
(`LocalStore.Pull` / `pullLocked` / `fetchAndMergeLocked`) was *not* exercised against a
shallow clone.

The implementation must cover this: if `Pull` on a shallow clone misbehaves (deepening
unexpectedly, or erroring on the `fetchAndMergeLocked` reset path), fix it or make the
`FetchOptions` carry the same `Depth`. Do not ship a default that breaks the pull path.

## Acceptance criteria

- `RemoteConfig.Depth` exists, loaded from `NTN_GIT_DEPTH`, defaulting to 1.
- `cloneFromRemote()` passes it as `CloneOptions.Depth`.
- The queue `RemoteConfig` in `internal/cmd/app.go` copies `Depth`.
- Unit tests for env parsing: unset -> 1, `0` -> 0, `5` -> 5, garbage -> 1.
- An integration-style test against a local temporary bare repository that: clones shallow,
  writes a file, commits, **pushes**, then **pulls** — all succeeding.
- Documented in `README.md` and `docs/cli-commands.md` (and `website/docs/cli-commands.md`
  if it carries the same env-var table), alongside the other `NTN_*` variables.
- `go build ./...`, `golangci-lint run ./...` and `go test ./...` all pass.
