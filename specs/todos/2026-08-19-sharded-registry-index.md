# Sharded registry index

**Date**: 2026-08-19
**Status**: Approved 2026-08-20 — pending implementation
**Companion to**: `2026-08-19-oom-large-repo.md` (fix S1a)

## Problem

`.notion-sync/ids/` stores one small JSON file per entity, in a single flat directory.
On the reference workspace that is **41,086 files / 162 MB — 52% of every file in the repo**:

| Prefix | Count |
|---|---|
| `page-*.json` | 31,399 |
| `file-*.json` | 9,568 |
| `user-*.json` | 119 |

This is the dominant cost in the repo, for a reason that is not obvious:

> **The `.notion-sync/ids` git tree object is 2.88 MB, and 8,361 commits touch it.**

Git rewrites a directory's entire tree object whenever any file inside it changes. A flat
directory with 41,086 entries therefore emits a fresh ~2.9 MB tree object on every sync
commit. That is ~24 GB of raw tree objects across the history.

It also explains the profile from the OOM investigation directly: `object.(*Tree).Decode`
was **55.8 GB — 82% of all allocation** during clone/checkout. That is this tree, decoded
over and over.

## Design

### Layout

```
.notion-sync/ids/
  page/00.jsonl  01.jsonl  …  ff.jsonl     256 shards, ~123 records, ~43 KB each
  file/00.jsonl  01.jsonl  …  ff.jsonl     256 shards, ~37 records each
  user.jsonl                                119 records, not worth sharding
```

- **Shard key**: first two hex characters of the *normalized* (dash-less) ID.
  Notion IDs are UUIDs, so the distribution is uniform — no hashing needed.
- **Format**: JSON Lines. One compact record per line, keys sorted, no indentation.
- **Ordering**: records sorted by `id` within a shard. Deterministic output means a
  one-record change is a one-line diff, which is what makes the delta compression work.

### Record

Unchanged from today's `PageRegistry` / `FileRegistry` / `UserRegistry`, minus two fields:

- Drop `ntnsync_version` per record. It is identical across all 41,086 records and a
  version bump currently rewrites every one of them. Put it once in a
  `.notion-sync/ids/manifest.json` alongside a `schema_version`.
- Drop the query string from `FileRegistry.SourceURL`. It stores a full pre-signed S3 URL
  (~2 KB of AWS credentials, signature and expiry) that **is written but never read**
  — `grep` finds only the write at `internal/sync/file.go:284` and the field declaration.
  It also rotates on every Notion fetch, so it churns the record even when the file is
  unchanged. Keep the bare object path, or drop the field.

  That alone removes ~19 MB and most of the `file-*` churn.

### Access

| Operation | Today | Sharded |
|---|---|---|
| `loadPageRegistry(id)` | open 1 file (after a 3-way fallback probe) | read+parse 1 shard (43 KB), served from an LRU of ~32 shards |
| `savePageRegistry(reg)` | write 1 file | read-modify-write 1 shard |
| `listPageRegistries()` | `List()` a 31,399-entry dir + 31,399 reads | read 256 shards sequentially |

`listPageRegistries` is called from `list.go` (×2), `cleanup.go`, `pull.go` and `path.go`,
so the full scan has to stay cheap — 256 sequential reads is strictly better than 31,399.

Writes should be **batched per commit cycle**: collect dirty records in memory, group by
shard, and flush once before the commit. Otherwise a sync touching 50 pages rewrites the
same shard many times.

### Migration

`loadPageRegistry` already probes three historical layouts
(`internal/sync/registry.go:70-98`). Add the shard lookup as the primary, and keep the
existing chain as the fallback:

1. `page/{id[:2]}.jsonl` — new canonical
2. `page-{normalized}.json` — current
3. `page-{dashed}.json` — legacy
4. `{normalized}.json` — oldest

Then a one-shot `ntnsync reindex --compact` that reads every legacy file, writes the
shards, and deletes the old files in one commit. Readers on an older binary keep working
until that commit lands; after it, the fallbacks are dead code and can be dropped a
release later.

## Measured effect

Synthetic reproduction of the real shape — 31,399 page records, then 60 commits each
touching 3 records (one hour at `NTN_COMMIT_PERIOD=1m`):

| | files in tree | pack after 60 commits |
|---|---|---|
| one file per page (current) | 31,399 | **12,868 KB** |
| sharded JSONL, 256 shards | 257 | **3,412 KB** |

**50× fewer files and a 3.8× smaller pack.** The pack shrinks because the 2.9 MB tree
object stops being rewritten on every commit, and because sorted JSONL shards delta
against their previous version, while thousands of separate small blobs cannot.

On the real repo this takes the checkout from 79,623 files to ~38,800 — a 51% reduction —
which is what removes the `Tree.Decode` hotspot behind the OOM.

## Choosing the shard count

The trade-off is tree size versus bytes rewritten per commit:

| Shards | Records/shard | Shard size | Files in tree |
|---|---|---|---|
| 16 | 1,962 | ~690 KB | 16 |
| **256** | **123** | **~43 KB** | **256** |
| 4,096 | 8 | ~3 KB | 4,096 |

256 is the sweet spot: shards stay small enough that rewriting one is cheap after delta
compression, while the directory tree stays trivial. 4,096 gives smaller writes but starts
recreating the many-small-files problem; 16 makes each write rewrite a 690 KB blob.

## Why not the obvious alternatives

- **One big JSON/JSONL file.** 162 MB rewritten on every commit, once a minute. The pack
  would grow faster than today's layout, and no useful diff.
- **SQLite.** Binary, whole-file rewrite per commit, no meaningful diff, and merge
  conflicts between two syncing replicas are unresolvable. Fine as a local cache, wrong as
  the committed source of truth.
- **Nested directories by ID prefix** (`ids/ab/cd/page-….json`). Fixes the single-huge-tree
  problem but keeps 41,086 files, so checkout and index cost barely move.
