# GitHub API storage (`NTN_STORAGE=github`)

An **opt-in** storage backend that talks to the GitHub Git Data API directly
instead of cloning the repository. There is no clone, no working tree and no
local filesystem write: memory scales with *how much changed*, not with how big
the repository is, and the process starts instantly.

It is never the default. It binds the deployment to one forge and is subject to
GitHub's API rate limits.

## Enabling it

```bash
export NTN_STORAGE=github
export NTN_GIT_URL=https://github.com/owner/repo.git   # must be github.com
export NTN_GIT_PASS=$GITHUB_TOKEN                       # contents: read & write
export NTN_GIT_BRANCH=main
export NTN_QUEUE_BRANCH=queue                           # optional, as usual
```

Startup fails immediately with an actionable error when `NTN_GIT_URL` is not a
`github.com` repository URL or `NTN_GIT_PASS` is empty. `NTN_DIR` /
`--store-path` are ignored: nothing is written to disk.

`ntnsync remote show` and `ntnsync remote test` both understand the mode;
`remote test` verifies the token and repository through the API.

## What is supported

| Command | Status |
|---------|--------|
| `serve` | Supported — the webhook-driven incremental path this backend exists for |
| `pull`, `sync`, `get`, `scan`, `status`, `list` | Supported |
| `reindex` (and `reindex --compact`) | **Rejected**, names the mode |
| `cleanup` | **Rejected**, names the mode |
| `list --tree` | **Rejected**, names the mode |

The rejected commands need a walk of the entire repository tree. GitHub's
recursive tree endpoint comes back **truncated** on repositories of the size
this backend targets (measured: `truncated: true` at 51,661 entries), so a
whole-tree walk cannot be served correctly. They fail fast rather than
half-working, and remain available on the git backend.

**Operational note:** migrating a repository to the sharded registry index
(`reindex --compact`) must be done on the git backend *before* switching a
deployment to `github` mode.

## How reads work

Reads descend the tree lazily, one directory at a time
(`GET /git/trees/{sha}`, never `recursive=1`), then fetch the leaf blob. A
bounded LRU caches directory listings keyed by **tree SHA** — git objects are
content-addressed, so a cached entry can never be stale, only evicted.

Blob bodies are deliberately *not* cached, except `.notion-sync/ids/**`
registry shards, which are small, hot and read repeatedly within a cycle.
Caching page content would reintroduce the memory problem this backend avoids.

This is why the sharded registry index (v0.9.0) is a prerequisite: reading page
metadata costs one shard fetch rather than a walk of a 41,086-entry directory.

## How writes work

Writes are buffered in memory. `Commit` performs:

1. `POST /git/blobs` — one per changed file (base64)
2. `POST /git/trees` — with `base_tree: <current root tree>` and one entry per
   change. Deletions are entries with `sha: null`.
3. `POST /git/commits` — with the new tree and the current HEAD as parent
4. `PATCH /git/refs/heads/{branch}` — with **`force: false`**, always

`base_tree` is what keeps a commit O(changed files): unchanged paths are
inherited and never transferred.

Reads on the store see the open transaction's buffered writes before falling
back to the API. Without that, a read-modify-write of the same registry shard
twice inside one commit cycle would lose the first write.

**Conflicts.** If the branch moved underneath us, the ref update is rejected.
The backend re-reads HEAD, rebuilds the tree on the new base and retries with
bounded attempts and backoff; the blobs from step 1 are already uploaded and
stay valid. `force: true` is never sent — it would silently discard the
concurrent writer's commit.

`Push` is a no-op (a committed transaction is already on the remote) and `Pull`
refreshes the cached HEAD.

## Rate limits

The authenticated core limit is 5,000 requests/hour. `X-RateLimit-Remaining`
and `X-RateLimit-Reset` are read from every response; when the remaining budget
falls to the floor the backend blocks until the window resets rather than
failing the sync. `429` and `403` with an exhausted budget are retried with
backoff. The budget is logged at INFO once per cycle.

A steady webhook-driven cycle is a handful of requests per minute and fits
comfortably. A cold full crawl does not — see the table above.

## Limits

- GitHub only. The API surface sits behind an internal interface so another
  forge is possible later, but none is implemented.
- Attachments are stored as blobs in the repository, as with the git backend.
- A single transaction's buffered payload is capped (64 MiB of base64-encoded
  bytes); exceeding it is a clear error rather than unbounded growth.
