---
model: sonnet
effort: low
---

# Lower the default max attachment size to 512 KB

**Date**: 2026-08-20
**Parent**: `specs/2026-08-19-oom-large-repo.md` (S1b)

## Problem

`defaultMaxFileSize` is 5 MB (`internal/sync/file.go:28`). On the reference workspace that
lets a small number of very large binaries dominate the repository:

| | files | bytes |
|---|---|---|
| all files | 79,623 | 1.22 GB |
| **over 512 KB** | **480 (0.6%)** | **0.69 GB (56%)** |
| remaining | 79,143 | 0.53 GB |

480 files — 0.6% of the tree — carry 56% of all content bytes. They inflate the packfile,
the clone time and the peak memory of every checkout.

## Proposal

Change the default from `5 * bytesPerMB` to `512 * bytesPerKB` in
`internal/sync/file.go`. `NTN_MAX_FILE_SIZE` continues to override it, so anyone who wants
the old behaviour sets `NTN_MAX_FILE_SIZE=5MB`.

Skipped attachments already log a warning and remain reachable through their Notion URL —
no behaviour change beyond the threshold itself.

Update the documented default everywhere it appears:

- `README.md`
- `docs/cli-commands.md`
- `website/docs/cli-commands.md`

Check each file for the literal `5MB`/`5 MB` default and for any prose describing it.

## Acceptance criteria

- `defaultMaxFileSize` is `512 * bytesPerKB`.
- A unit test asserts the default resolves to 524288 when `NTN_MAX_FILE_SIZE` is unset.
- A unit test asserts `NTN_MAX_FILE_SIZE=5MB` still overrides it.
- All three docs state 512 KB as the default; no stale "5MB" default remains
  (`grep -rn "5MB\|5 MB" README.md docs/ website/docs/` shows no default claim).
- `go build ./...`, `golangci-lint run ./...` and `go test ./...` all pass.
