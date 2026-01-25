# Root Folder Organization

## Overview

This spec defines how notion-sync organizes content into two types of root folders:
1. **Explicit root documents** - specified via `--space`, stored with `index.md` as entry point
2. **Teamspaces** - discovered via global sync, root documents prefixed with `_` for visibility

## Root Document Types

### Explicit Root Documents (`--space`)

When a user explicitly specifies a root document via `--space`:
- A dedicated folder is created for it
- The root document is saved as `index.md` within that folder
- All children are stored within the same folder hierarchy

Example:
```
output/
└── my-project/
    ├── index.md          # Root document
    ├── getting-started.md
    └── architecture/
        └── index.md
```

### Teamspace Root Documents (`global-sync`)

When syncing via global-sync, teamspace root documents:
- Are prefixed with `_` to distinguish them and sort them at the top
- Each teamspace gets its own folder

Example:
```
output/
└── engineering/
    ├── _project-roadmap.md    # Teamspace root doc
    ├── _team-handbook.md      # Teamspace root doc
    ├── feature-specs/
    │   └── index.md
    └── meeting-notes/
        └── index.md
```

## Global Sync Command

A new `global-sync` command will:

1. **List all teamspaces** via Notion API
2. **For each teamspace**:
   - Create a folder named after the teamspace
   - Register the teamspace in `ids/space-$spaceId.json` with folder mapping
   - Query root documents where `parent.type == "workspace"`
   - Queue each root document for indexing

### Teamspace Registry File

File: `ids/space-{spaceId}.json`

```json
{
  "space_id": "abc123",
  "name": "Engineering",
  "folder": "engineering",
  "synced_at": "2026-01-15T10:00:00Z"
}
```

## Child Document Resolution

When fetching children documents:

1. **Check if document already exists** in the registry
   - If yes: keep the existing location (do not move)
   - If no: continue to step 2

2. **Place in parent's folder**
   - Fetch the parent's location from the registry
   - Store the child in the same folder as its parent

This ensures:
- Documents don't get duplicated across folders
- Existing document locations are preserved
- New documents follow their parent hierarchy

## Implementation Notes

### Folder Naming

Root folder names are derived from the document or teamspace title with the following transformations:
- Spaces are replaced with dashes (`-`)
- Names are lowercased
- Special characters are removed or simplified

Example: "My Project Docs" → `my-project-docs/`

### File Naming

| Document Type | Naming Pattern |
|---------------|----------------|
| Explicit root (`--space`) | `index.md` |
| Teamspace root | `_document-title.md` |
| Child with children | `folder/index.md` |
| Leaf child | `document-title.md` |

### API Calls

For `global-sync`:
```
GET /v1/users/me → get user info and available spaces
GET /v1/search → filter by parent.type == "workspace" for root docs
```

For child resolution:
```
GET /v1/blocks/{page_id}/children → get page content
GET /v1/pages/{page_id} → get parent info if needed
```
