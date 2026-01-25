# Clean Filenames with Registry Index

**Date:** 2026-01-15
**Status:** Proposal

## Problem

Markdown files currently include a truncated Notion ID in their filename (e.g., `my-page-abc12345.md`). This makes filenames less readable and harder to work with in editors and git diffs.

## Goal

Have clean, human-readable filenames while maintaining a registry that maps Notion IDs to files:

```
notion/
  private/
    _root-page.md
    child-page.md
  teamspace-name/
    _team-root.md
    team-child.md
  .notion-sync/
    ids/
      page-abc123def456.json
      page-xyz789uvw012.json
      space-space123id.json
```

## Registry Structure

### Page Registry: `.notion-sync/ids/page-${notion_id}.json`

```json
{
  "id": "abc123def456",
  "space_id": "space123id",
  "file_path": "private/_root-page.md",
  "title": "Root Page",
  "last_edited": "2026-01-15T10:30:00Z",
  "is_root": true,
  "parent_id": ""
}
```

### Space Registry: `.notion-sync/ids/space-${notion_id}.json`

```json
{
  "id": "space123id",
  "name": "My Teamspace",
  "directory": "my-teamspace"
}
```

For private workspace (no space_id):
```json
{
  "id": "",
  "name": "Private",
  "directory": "private"
}
```

## Filename Conflict Resolution

When two pages have the same sanitized title within the same space, append a 4-character ID suffix to the conflicting page:

```
notion/
  private/
    meeting-notes.md          # First page with this title
    meeting-notes-xyz7.md     # Conflicting page (different notion_id)
```

**Rules:**
- The first page discovered with a given name keeps the clean filename
- Subsequent pages with the same name get a `-{short_id}` suffix (first 4 chars of notion_id)
- Conflict detection is case-insensitive

## File Renaming Policy

**Important:** When a page's title changes in Notion, the markdown file is NOT automatically renamed during sync. The file keeps its original name and only the content is updated.

Renaming files should be a separate, explicit operation (future `rename` command) to:
- Avoid unexpected git churn
- Allow users to maintain their own naming conventions
- Prevent accidental file path changes in references

## Frontmatter Format

Each markdown file includes Notion metadata in YAML frontmatter:

```markdown
---
notion_id: abc123def456789
notion_space_id: space123id
last_edited: 2026-01-15T10:30:00Z
notion_parent_id: parent123id
notion_url: https://notion.so/abc123def456789
---

# Page Title

Content here...
```

This enables:
- Full rebuild of registry from files alone
- Git-friendly metadata (visible in diffs)
- Easy scripting and tooling

## Reindex Command

### Usage

```bash
notion-sync reindex [--dry-run]
```

### Behavior

1. **Scan all `.md` files** in the store directory
2. **Extract Notion metadata** from frontmatter
3. **Build registry** from scanned files
4. **Handle duplicates**: If two files have the same `notion_id`:
   - Keep the file with the latest `last_edited` timestamp
   - Delete the older file
   - Log the resolution
5. **Write registry** to `.notion-sync/ids/`

### Dry Run Output

```
$ notion-sync reindex --dry-run

Scanning files...
Found 42 markdown files

Registry changes:
  CREATE ids/page-abc123.json -> private/_root-page.md
  CREATE ids/page-def456.json -> private/child-page.md
  CREATE ids/space-xyz789.json -> my-teamspace/

Duplicates found:
  CONFLICT: notion_id=ghi789
    - private/old-meeting.md (edited: 2026-01-10)
    - private/meeting-notes.md (edited: 2026-01-15) [KEEP]
  ACTION: Would delete private/old-meeting.md

Summary: 42 pages, 1 space, 1 duplicate to resolve
```

## Design Decisions

- **YAML frontmatter**: Widely supported by editors and static site generators
- **Individual JSON registry files**: Easier git merges, atomic updates per page
- **Case-insensitive conflict detection**: Avoids filesystem issues on macOS/Windows
- **Conservative filename sanitization**: Alphanumeric characters and hyphens only
