# OOM crashes on large repositories

**Date**: 2026-08-19
**Status**: Diagnosed — plan approved 2026-08-20, split into implementable specs

> **This is the umbrella roadmap, not a unit of work.** It is deliberately *not* in
> `specs/todos/`: the implementable parts have been split into their own specs (see
> "Disposition" below), and the remaining parts are either out-of-repo or must not be
> automated.

## Symptom

`ntnsync serve` restarts repeatedly when backing up a large Notion workspace.
Container state: `Reason: OOMKilled`, `Exit Code: 137`, 7 restarts in 5 days
against a `memory: 1Gi` limit.

## Measurements

Reference repository used for all measurements below:

| Metric | Value |
|---|---|
| Worktree | 1.4 GB / **79,623 files** |
| `.git` | 1.1 GB |
| Commits (content branch) | 36,412 |
| Commits (queue branch, orphan) | 31,392 |
| `.notion-sync/ids/` | **41,086 files** / 162 MB (52% of all files) |
| — of which `page-*.json` | 31,399 |
| — of which `file-*.json` | 9,568 |
| — of which `user-*.json` | 119 |
| Binary attachments (png/mov/mp4/docx/pdf/zip) | **~1.07 GB** (png alone 800 MB / 3,457 files) |
| Actual documents (md + json content) | ~338 MB |

Observed in-cluster (limit temporarily raised to 3Gi to measure):

| Phase | Working set |
|---|---|
| Startup clone + checkout | 1.2 – 1.5 GB, **9.5 minutes** |
| Steady state | **1.7 – 1.8 GB** |

The workload no longer fits in 1Gi at any point in its lifecycle.

## Root causes

### 1. Startup clone + checkout dominates (primary)

`heap` profile, in-use: **49% of live heap** is `LocalStore.cloneFromRemote`
(`packfile.Parser.get` + `plumbing.MemoryObject.Write`).

`allocs` profile, cumulative: **68 GB allocated, 93% under `cloneFromRemote`**, of which
**`object.(*Tree).Decode` is 55.8 GB (82% of all allocation)**, reached via
`Worktree.Reset` → `resetWorktree` → `checkoutChange` → `Tree.FindEntry` → `Tree.dir`.

go-git re-decodes intermediate tree objects for every path it checks out. With 79,623
files at 8+ levels of nesting this is effectively quadratic.

`internal/store/local.go` `cloneFromRemote()` uses `PlainClone` with `SingleBranch: true`
but **no `Depth`**, so all 36,412 commits are fetched and parsed.

### 2. The data directory has no persistent volume

When `NTN_DIR` points at ephemeral container storage, every restart re-clones the whole
repository — so cause #1 fires on every restart, making the OOM self-sustaining
(crash → re-clone → OOM).

It also means the freshly written checkout is charged to the cgroup as page cache: Go's
own `Sys` was only ~750 MiB of a 1.5 GB working set.

### 3. Full-worktree scan on every commit cycle

`localTransaction.Commit()` (`internal/store/local.go:534`) does:

```go
worktree.AddWithOptions(&git.AddOptions{All: true})  // full merkletrie walk
status, err := worktree.Status()                     // second full walk
```

Measured: **640 MiB allocated per `Status()` call, ~1.28 GB per commit cycle**, at
`NTN_COMMIT_PERIOD=1m`. Peak RSS ~250 MB per cycle on top of baseline.

The transaction **already tracks `modifiedPaths`** precisely (`local.go:413/470/491`) and
then throws it away.

### 4. Two full clones

`NTN_QUEUE_BRANCH` creates a second `LocalStore` (`internal/cmd/app.go:868`) that does its
own full clone of the same remote, with its own go-git object cache (96 MiB LRU default
each).

### 5. No `GOMEMLIMIT`

Go's GC has no knowledge of the cgroup limit, so it lets the heap grow on the default
GOGC=100 schedule and is killed before it collects.

## Approved plan

All tiers below were reviewed and approved on 2026-08-20. They are ordered by the sequence
they should land in, not by importance.

### Tier 1 — unblock now (deployment only, no code change)

| Fix | Detail | Effect |
|---|---|---|
| Raise the memory limit | 2.5–3Gi | Stops the crash loop immediately. |
| Set `GOMEMLIMIT` + `GOGC=50` | `GOMEMLIMIT` ~85% of the container limit | Makes the GC respect the cgroup ceiling instead of being killed before it collects. |
| **Persistent volume claim, 20 GB** | mounted at `NTN_DIR`, plus `${NTN_DIR}-queue` when `NTN_QUEUE_BRANCH` is set | Removes the re-clone on every restart: startup becomes a `Pull`, not a 9.5-min 1.5 GB clone. Biggest single operational win. |

20 GB gives comfortable headroom over the current 2.5 GB checkout (1.4 GB worktree +
1.1 GB `.git`) plus the queue clone, and room for `git gc` to hold both old and new packs
during repacking.

### Tier 2 — cheap code fixes (validated by benchmark)

**2a. Stage only modified paths.** Replace `Add(All)` + `Status()` with the already-tracked
`modifiedPaths`, using `AddWithOptions{Path: p, SkipStatus: true}` (which skips the full
`Status()` walk for regular files) and `Worktree.Remove(p)` for deletions.

Measured on the reference repo, 20 changed files:

| | elapsed | peak RSS |
|---|---|---|
| current `Add(All)`+`Status()` | 3.14 s | 252 MB |
| targeted staging | **0.11 s** | **63 MB** |

Caveat: each targeted `Add` re-encodes the whole 79k-entry index, so allocation churn per
*call* is high — fall back to `All: true` above a threshold (e.g. >500 changed paths).

**2b. Shallow clone.** Add `NTN_GIT_DEPTH` (default 1) -> `CloneOptions.Depth`.
Verified end-to-end: shallow clone -> targeted add -> commit -> **push from shallow works**
with go-git v5.19.2. Clone peak RSS 1.32 GB -> 705 MB.

### Tier 3 — the actual scaling fix

**S1a — Sharded registry index.** Consolidate `.notion-sync/ids/` (41,086 tiny JSON files)
into 256 sorted JSONL shards. Full design, measurements and migration path in the
companion spec `2026-08-19-sharded-registry-index.md`.

Measured: **50x fewer files and a 3.8x smaller pack**, because a flat 41,086-entry
directory emits a fresh 2.88 MB tree object on every one of the 8,361 commits that touch
it — which is precisely the `Tree.Decode` hotspot behind the OOM.

**S1b — Cap attachments at 512 KB.** Lower the `NTN_MAX_FILE_SIZE` default from 5 MB to
**512 KB**.

Measured against the reference worktree:

| | files | bytes |
|---|---|---|
| all files | 79,623 | 1.22 GB |
| **over 512 KB** | **480 (0.6%)** | **0.69 GB (56%)** |
| remaining | 79,143 | 0.53 GB |

480 files carry 56% of all content bytes. Skipping them is the cheapest large win
available; oversized attachments stay reachable through their Notion URL.

**S1c — Rewrite the history.** S1a and S1b only change what is written from now on; the
existing 1.1 GB pack still carries every superseded 2.88 MB tree object and every large
binary ever downloaded. Reclaim it with a history rewrite (`git filter-repo`, or a fresh
orphan branch seeded from the current tree if history is not worth preserving), then
force-push and have every replica re-clone.

Do this **after** S1a + S1b are deployed and stable, so the rewrite has to happen once.

Combined effect of S1a + S1b + S1c: roughly **2.5 GB / 79.6k files -> ~0.4 GB / ~38.4k
files**, with a freshly packed history.

**S2 — Pure git, remote-only, via the real `git` binary.**
`git clone --filter=blob:none --depth=1 --single-branch` plus sparse checkout. Real git
streams packfiles and keeps memory ~100 MB regardless of repo size.
go-git v5.19.2 has **no partial-clone/filter support** (only `Depth`), so this requires
shelling out to `git` and dropping the distroless base (+~15 MB for a git binary).
Keeps the tool host-agnostic: any git remote still works.

**S3 — Forge API backend, as an optional mode.**
GitHub Git Data API: create blobs -> tree with `base_tree` -> commit -> update ref. Memory
is O(changed files), no clone, no disk, instant startup — a natural fit for the
webhook-driven incremental case.

Ships as an **opt-in storage mode** (e.g. `NTN_STORAGE=github`) beside the existing git
backend, never as the default: it ties the backend to one forge and is subject to API rate
limits. S1a is a prerequisite — reading existing state means fetching a handful of shards
rather than 41,086 individual files.

## Sequencing

1. **Tier 1** — deployment change, stops the bleeding today, no code risk.
2. **Tier 2** — small, benchmarked, keeps pure git.
3. **S1a + S1b** — the real fix.
4. **S1c** — one history rewrite once 3 is stable.
5. **S2** — removes the remaining go-git memory ceiling.
6. **S3** — optional mode, once S1a has landed.

## Diagnostic support added

`NTN_PPROF=true` / `--pprof` exposes `net/http/pprof` on the webhook port
(`internal/webhook/server.go`, `internal/webhook/config.go`, `internal/cmd/app.go`).
Off by default.

## Resolved open questions

Answered 2026-08-20. Each answer is a directive, not a suggestion.

> **Q1.** S1c: "Reclaim it with a history rewrite (`git filter-repo`, or a fresh orphan
> branch seeded from the current tree if history is not worth preserving), then force-push
> and have every replica re-clone."

**Decision: do not automate this, and do not implement it now.** A history rewrite
force-pushes over a live backup repository and decides whether 36,412 commits survive — it
is irreversible. It stays a manual, supervised operation, run once, only after S1a and S1b
are deployed and stable. The method (`filter-repo` vs fresh orphan branch) is chosen at that
time, when it is known whether the history still has value.

> **Q2.** Tier 1 names a memory limit, `GOMEMLIMIT`/`GOGC`, and a 20 GB PVC — but which
> repository owns them?

**Decision: split it.** The memory limit, the env vars and the PVC live in the deployment
manifest, which is in the ops repository; this repository contains no manifests. They ship
as a change there, by hand.

The one part that belongs in this repository is having the binary discover the cgroup limit
and set `GOMEMLIMIT` itself — that fixes it for every deployment rather than one. Split out
as `specs/todos/2026-08-20-gomemlimit-from-cgroup.md`.

> **Q3.** "Raise the memory limit | 2.5–3Gi" and "`GOMEMLIMIT` ~85% of the container limit"
> are a range and an approximation.

**Decision: 3Gi container limit, `GOMEMLIMIT` = 2560 MiB (85%), `GOGC=50`.** Applied in the
ops repository per Q2. The in-repo helper uses the same 85% factor.

> **Q4.** S2 and S3 describe outcomes but no implementation.

**Decision: neither is in scope yet.** S2 (drop the distroless base, replace the go-git
store with an exec'd `git` using `--filter=blob:none`) and S3 (a second storage backend on
the forge API) are each a project, and this spec's own Sequencing puts them at steps 5 and
6. Each needs its own spec, written when its turn comes. They are recorded here as
direction, not as work.

> **Q5.** (in the companion spec) `FileRegistry.SourceURL`: "Keep the bare object path, or
> drop the field."

**Decision: keep the field, store only the bare object path** — strip the query string.
Recorded in `specs/todos/2026-08-20-sharded-registry-index.md`.

## Disposition

| Item | Where it lives now |
|---|---|
| Tier 1 — container limit, `GOGC`, 20 GB PVC | Ops repository, by hand (Q2, Q3) |
| Tier 1 — `GOMEMLIMIT` from cgroup | `specs/todos/2026-08-20-gomemlimit-from-cgroup.md` |
| Tier 2a — targeted staging | `specs/todos/2026-08-20-git-targeted-staging.md` |
| Tier 2b — shallow clone | `specs/todos/2026-08-20-git-shallow-clone.md` |
| S1a — sharded registry index | `specs/todos/2026-08-20-sharded-registry-index.md` |
| S1b — 512 KB attachment cap | `specs/todos/2026-08-20-max-file-size-512kb.md` |
| S1c — history rewrite | **Parked.** Manual, supervised, after S1a+S1b are stable (Q1) |
| S2 — real `git` binary backend | **Parked.** Needs its own spec (Q4) |
| S3 — forge API backend | **Parked.** Needs its own spec (Q4) |
