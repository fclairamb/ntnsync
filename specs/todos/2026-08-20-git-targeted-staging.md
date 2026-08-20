---
model: opus
effort: high
---

# Stage only modified paths instead of scanning the whole worktree

**Date**: 2026-08-20
**Parent**: `specs/2026-08-19-oom-large-repo.md` (Tier 2a)

## Problem

`localTransaction.Commit()` in `internal/store/local.go` (around line 517) stages with:

```go
worktree.AddWithOptions(&git.AddOptions{All: true})  // full merkletrie walk
status, err := worktree.Status()                     // second full walk
```

Both are whole-worktree operations. On the reference repository (79,623 files) each
allocates ~640 MiB, so every commit cycle churns ~1.28 GB — once a minute at
`NTN_COMMIT_PERIOD=1m`.

The transaction **already tracks exactly which paths changed**: `modifiedPaths` is
populated by `Write` (line ~413), `WriteStream` (~470) and `Delete` (~491), and then
discarded by `Commit`.

## Proposal

Stage from `modifiedPaths` instead of walking the tree.

For each path in `modifiedPaths`:

- If the file exists on disk: `worktree.AddWithOptions(&git.AddOptions{Path: p, SkipStatus: true})`
- If it does not exist (deleted): `worktree.Remove(p)`

`SkipStatus: true` is the load-bearing detail. In go-git v5.19.2, `doAdd` only calls the
expensive `w.Status()` when `skipStatus` is false, or the path is a directory, or `Lstat`
fails — so a regular file with `SkipStatus: true` skips the full walk entirely.
`Worktree.Remove` never calls `Status()`.

Determine `hasChanges` from whether anything was actually staged, replacing the current
`Status()`-based check. Keep the existing behaviour of returning nil (and clearing
`modifiedPaths`) when there is nothing to commit.

### Threshold fallback

Each targeted `Add` reads and re-encodes the entire index (79k entries), so per-call
allocation is high. Above a threshold the whole-tree walk is cheaper. Add a package-level
const (e.g. `maxTargetedStagingPaths = 500`) and fall back to the existing
`AddWithOptions{All: true}` + `Status()` path when `len(modifiedPaths)` exceeds it.

### Correctness details to get right

- go-git wants **slash-separated paths relative to the worktree root**. `modifiedPaths`
  keys are already store-relative — confirm they need no `filepath.ToSlash` conversion on
  the platforms targeted, and convert if they do.
- `worktree.Remove(p)` on a path that is not in the index returns an error. A path can be
  written and then deleted within one transaction, so it may never have been committed.
  Tolerate "not in index" rather than failing the commit.
- A path may be listed in `modifiedPaths` while its content is identical to HEAD. Staging
  it is a no-op for the resulting tree — that is fine, but it must not be counted as a
  change, or the code will create empty commits.
- Preserve the existing locking: `Commit` takes `t.mu` then `t.store.mu`.

## Measured effect

Reference repository, 20 changed files:

| | elapsed | peak RSS |
|---|---|---|
| current `Add(All)`+`Status()` | 3.14 s | 252 MB |
| targeted staging | **0.11 s** | **63 MB** |

## Acceptance criteria

- `Commit()` stages from `modifiedPaths` and no longer calls `Status()` on the common path.
- Threshold fallback to `All: true` above `maxTargetedStagingPaths`.
- Tests covering, against a temporary repository:
  - write a new file -> commit -> file is in HEAD
  - modify an existing file -> commit -> new content in HEAD
  - delete a tracked file -> commit -> path absent from HEAD
  - write then delete the same path within one transaction -> commit succeeds
  - no changes -> `Commit` is a no-op and creates no commit
  - more than the threshold number of paths -> still commits correctly (fallback path)
- Existing store tests continue to pass unchanged.
- `go build ./...`, `golangci-lint run ./...` and `go test ./...` all pass.

## Implementation Plan

1. **Targeted staging helper** — add `stageModifiedPaths` on `localTransaction`:
   iterate `modifiedPaths`, `os.Lstat` the on-disk path to classify add vs delete,
   then `worktree.AddWithOptions(&git.AddOptions{Path: p, SkipStatus: true})` for
   existing files and `worktree.Remove(p)` for missing ones. Tolerate
   `index.ErrEntryNotFound` from `Remove` (write-then-delete inside one transaction).
   Convert keys with `filepath.ToSlash` for go-git and `filepath.FromSlash` +
   `filepath.Join(rootPath, ...)` for the `Lstat`.

2. **Change detection without `Status()`** — add `stagedPathsDiffer`, comparing, for
   each tracked path, the index entry hash against the HEAD tree entry hash
   (`repo.Storer.Index()` vs `HEAD` commit tree `FindEntry`). Presence mismatch or
   hash mismatch means a real change; identical means the path was touched but its
   content matches HEAD, so no empty commit is produced. A repository with no HEAD
   (no commit yet) treats any index entry as a change.

3. **Threshold fallback** — package const `maxTargetedStagingPaths = 500`. When
   `len(modifiedPaths)` exceeds it, keep the existing `AddWithOptions{All: true}` +
   `Status()` scan, extracted into its own helper so `Commit` stays under the
   `funlen` budget.

4. **Wire into `Commit`** — keep the `t.mu` then `t.store.mu` lock order and the
   existing "nothing to commit -> clear `modifiedPaths`, return nil" behaviour.

5. **Tests** in `internal/store/targeted_staging_test.go` covering the six spec
   scenarios plus a `filepath.ToSlash`/nested-directory case, using a temporary
   `NewLocalStore` and reading back the HEAD tree to assert.

6. **QA** — `go build ./...`, `golangci-lint run ./...`, `go test ./...`.
