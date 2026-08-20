---
model: opus
effort: xhigh
---

# GitHub API storage backend (`NTN_STORAGE=github`)

**Date**: 2026-08-20
**Parent**: `specs/2026-08-19-oom-large-repo.md` (S3)
**Prerequisite**: S1a sharded registry index — shipped in v0.9.0

## Problem

Every current storage mode materialises the whole repository: a clone plus a
79,623-file checkout. That is the root of the OOM work in v0.9.0, and it scales
with *repository* size rather than with *how much changed*.

v0.9.0 got the reference deployment to 71Mi steady state, so this is no longer
urgent — but the ceiling is structural. A backend that talks to the forge API
directly has memory proportional to the changed set, no clone, no disk, and
starts instantly.

This ships as an **opt-in mode alongside** the git backend, never as the default:
it binds the tool to one forge and is subject to API rate limits.

## Hard constraints (measured, not assumed)

Measured against the reference workspace repository on 2026-08-20:

| Probe | Result |
|---|---|
| `GET /git/trees/{main}?recursive=1` | **`truncated: true`**, 51,661 entries |
| `GET /git/trees/{main}` (non-recursive) | `truncated: false`, 19 entries |
| `GET /rate_limit` core | **5,000 requests/hour** |

**The recursive tree fetch is unusable on a repository this size.** Any design
that starts with "fetch the tree once" is wrong. Reads must descend lazily,
one directory at a time, and cache what they see.

## Design

### Mode selection

`RemoteConfig.Storage` already exists with values `local` / `remote` (`NTN_STORAGE`,
`internal/store/remote.go`). Add `github`.

- Repository owner/name parsed from `NTN_GIT_URL`.
- Token from `NTN_GIT_PASS` (same secret the git backend uses).
- Branch from `NTN_GIT_BRANCH`; queue branch from `NTN_QUEUE_BRANCH` as today.

Reject at startup, with a clear error, if the URL is not a GitHub URL or the
token is missing.

### Writes — Git Data API

A `Commit` becomes:

1. `POST /repos/{o}/{r}/git/blobs` per changed file → blob SHA
   (`content` base64, `encoding: "base64"`)
2. `POST /repos/{o}/{r}/git/trees` with `base_tree: <current root tree sha>` and
   one entry per change → new tree SHA.
   Deletions are an entry with `sha: null`.
3. `POST /repos/{o}/{r}/git/commits` with `tree` and `parents: [<head>]`
4. `PATCH /repos/{o}/{r}/git/refs/heads/{branch}` with `force: false`

`base_tree` is what makes this O(changed files): unchanged paths are inherited
without ever being transferred.

**Conflict handling.** Step 4 fails if the ref moved underneath us. On failure:
re-read HEAD, rebuild the tree on the new base (steps 2–4; blobs from step 1
are already uploaded and still valid), retry with bounded attempts and backoff.
This replaces the git backend's pull-then-retry path. Never pass `force: true` —
that would silently discard a concurrent writer's commit.

### Reads — lazy tree descent

Maintain, per branch:

- HEAD commit SHA and its root tree SHA
- An LRU (bounded, e.g. 256 entries) of `directory path → {tree SHA, entries}`

`Read(path)` resolves the path by walking the cached directory chain, fetching
only the directories it has not seen (`GET /git/trees/{sha}`, non-recursive),
then `GET /git/blobs/{sha}` for the leaf. Never `recursive=1` at the root.

`Exists(path)` is the same walk without the blob fetch.

`List(dir)` is a single non-recursive tree fetch for that directory.

This is what makes the sharded index a prerequisite: reading page metadata costs
one shard fetch (`.notion-sync/ids/page/ab.jsonl`) instead of walking a
41,086-entry directory.

Blob contents are **not** cached by default — that reintroduces the memory
problem. Cache only `.notion-sync/ids/**` shard bodies, bounded, since they are
small, hot, and read repeatedly within a cycle.

### Transaction semantics

The current `Transaction` contract is "writes are applied immediately to the
filesystem; `Commit` creates a git commit". The API backend cannot apply
immediately — there is no filesystem.

Buffer instead: `Write`/`WriteStream`/`Delete` accumulate into an in-memory
change set keyed by path; `Commit` performs the blob/tree/commit/ref sequence and
clears it; `Rollback` discards it. `Mkdir` is a no-op — git has no directories.

Reads during an open transaction must see the transaction's own pending writes
before falling back to the API, or a read-modify-write of a shard within one
cycle will lose data. This is the single most important correctness property in
this spec.

Buffered writes are bounded by the existing 512 KB attachment cap, but base64
inflates payloads by 4/3 — account for it, and cap total buffered bytes with a
clear error rather than growing without limit.

### Interface work this requires

`Store` (`internal/store/store.go`) is already small enough — `Read`, `Exists`,
`List`, `BeginTx`, `Push`, `Lock`, `Unlock` — and `FS()` has **no non-test
callers**, so `ReadFSProvider` need not be implemented.

Two things do need generalising:

- **`SplitStore` is hard-typed to `*LocalStore`** (`internal/store/split.go`:
  `storeFor`, `ContentStore()`, `QueueStore()`). It must hold `Store` values so a
  GitHub store can back the content and queue branches.
- **`internal/cmd/app.go` type-switches on the concrete stores** for remote config
  and pull (`storeRemoteConfig` ~line 819, and the pull helper ~line 834).
  Replace with a small interface (e.g. `RemoteStore { Pull(ctx) error;
  IsRemoteEnabled() bool; RemoteConfig() *RemoteConfig }`) that all three
  implement, rather than adding a third case.

For the API backend, `Push` is a no-op (commits are already remote) and `Pull`
refreshes the cached HEAD and invalidates the tree cache.

### Rate limits

Read `X-RateLimit-Remaining` / `X-RateLimit-Reset` from every response. When
remaining falls below a floor, block until reset rather than failing the sync.
Log the budget at INFO once per cycle. Treat `403` with
`X-RateLimit-Remaining: 0` and `429` as retryable with backoff.

A steady webhook-driven cycle is a handful of requests per minute and fits
comfortably in 5,000/hour. A cold full crawl does not — see scope.

## Scope for v1

**Supported:** `serve` — the webhook-driven incremental path this backend is
actually for.

**Not supported**, and must fail fast with a clear message naming the mode:
`reindex` (walks every markdown file), `cleanup` and `list --tree` (whole-tree
enumeration). These need the truncation-proof full walk that this spec
deliberately avoids; they remain available on the git backend.

Do not silently degrade — an unsupported command must error, not half-work.

**Operational note:** migrating a repository to the sharded index
(`reindex --compact`) must therefore be done on the git backend *before*
switching a deployment to `github` mode.

## Acceptance criteria

- `NTN_STORAGE=github` selects the backend; invalid URL or missing token fails at
  startup with an actionable error.
- A full `Store` + `Transaction` implementation with no local filesystem writes
  (`grep` for `os.WriteFile|os.Create|os.MkdirAll` in the new package: zero hits).
- Reads never issue `recursive=1` against the root tree. A test asserts this.
- Reads inside an open transaction observe that transaction's pending writes.
  A read-modify-write of the same shard twice in one cycle keeps both records.
- A commit issues exactly: N blob POSTs, one tree POST with `base_tree`, one
  commit POST, one ref PATCH. A test counts requests against a stubbed API.
- Deletions are emitted as tree entries with `sha: null`, and the path is absent
  from the resulting tree.
- A ref-moved conflict triggers rebuild-and-retry, and never sends `force: true`.
  A test simulates a concurrent ref move.
- `SplitStore` holds `Store` values; the `internal/cmd/app.go` type switches are
  replaced by an interface. Existing git-backend behaviour is unchanged — the
  whole existing test suite still passes.
- Unsupported commands (`reindex`, `cleanup`, `list --tree`) fail fast naming the
  mode.
- Rate-limit headers are honoured, with a test for the below-floor wait path.
- Tests use a stubbed HTTP transport, not the live API.
- `go build ./...`, `golangci-lint run ./...` and `go test ./...` all pass.

## Explicitly out of scope

- Forges other than GitHub. Keep the API surface behind an internal interface so
  a second forge is possible later, but do not build one.
- Making this the default.
- Whole-tree operations (see Scope).
- Attachment storage anywhere other than blobs in the repository.
