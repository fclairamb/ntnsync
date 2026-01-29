# Extended Page and Database Properties

**Date:** 2026-01-29
**Status:** Proposed

## Problem

Currently, the markdown frontmatter for pages and databases only includes basic metadata:
- `title`
- `notion_type`
- `notion_folder`
- `file_path`
- `last_edited`
- `last_synced`
- `notion_parent_id`
- `is_root`
- `notion_url`

Important metadata from the Notion API is not being saved:
- Creator of the page
- Last editor of the page
- Icon (emoji or file)
- Database properties (for database pages)

## Proposed Solution

Add the following properties to the frontmatter:

### 1. Creator Information

Add `created_by` field with the user ID:

```yaml
created_by: "user-uuid-here"
```

### 2. Last Edited By

Add `last_edited_by` field:

```yaml
last_edited_by: "user-uuid-here"
```

### 3. Icon

Add `icon` field. The format depends on the icon type:

For emoji icons:
```yaml
icon: "emoji:📝"
```

For external file icons:
```yaml
icon: "external:https://example.com/icon.png"
```

For Notion-hosted file icons:
```yaml
icon: "file:https://s3.us-west-2.amazonaws.com/..."
```

### 4. Database Page Properties

For pages that are children of a database (database rows), save the database properties under a `properties` parent key. Properties should be flattened to their display values:

```yaml
properties:
  Status: "In Progress"
  Priority: "High"
  Due Date: "2026-02-15"
  Tags:
    - "feature"
    - "urgent"
  Assignee: "user-uuid-here"
```

## Implementation Details

### Type Struct Updates

The `Page` struct in `internal/notion/types.go` needs to add the `Icon` field (currently missing but returned by Notion API):

```go
type Page struct {
    // ... existing fields ...
    Icon           *Icon          `json:"icon"`
    Cover          *FileBlock     `json:"cover"`  // Also missing, for future use
    // ... rest of fields ...
}
```

### Converter Changes

Update `internal/converter/converter.go`:

1. In `generateFrontmatter()`, add:
   - `created_by` from `page.CreatedBy.ID`
   - `last_edited_by` from `page.LastEditedBy.ID`
   - `icon` formatted according to icon type

2. For database pages (when `opts.NotionType == "database_page"`), extract and format the properties from `page.Properties`.

### Property Value Extraction

Create a helper function to extract display values from database page properties:

```go
func extractPropertyValue(propData json.RawMessage) (any, error) {
    // Handle different property types:
    // - title, rich_text -> string
    // - number -> float64
    // - select -> string (name)
    // - multi_select -> []string (names)
    // - date -> string (start date)
    // - checkbox -> bool
    // - url, email, phone_number -> string
    // - people -> []string (user IDs)
    // - relation -> []string (page IDs)
    // - status -> string (name)
}
```

## Example Output

### Regular Page
```yaml
---
title: "My Page"
notion_type: page
notion_folder: tech
file_path: tech/my-page.md
created_by: "abc123"
last_edited_by: "def456"
last_edited: 2026-01-29T10:30:00Z
last_synced: 2026-01-29T11:00:00Z
icon: "emoji:📝"
notion_parent_id: xyz789
is_root: false
notion_url: https://notion.so/...
---
```

### Database Page
```yaml
---
title: "Task: Fix login bug"
notion_type: database_page
notion_folder: tech
file_path: tech/task-fix-login-bug.md
created_by: "abc123"
last_edited_by: "def456"
last_edited: 2026-01-29T10:30:00Z
last_synced: 2026-01-29T11:00:00Z
icon: "emoji:🐛"
notion_parent_id: database-id
is_root: false
notion_url: https://notion.so/...
properties:
  Status: "In Progress"
  Priority: "P1"
  Assignee: "abc123"
  Due Date: "2026-02-01"
  Tags:
    - "bug"
    - "auth"
---
```

## Backward Compatibility

- New fields are additive; existing frontmatter parsing will continue to work
- The reindex command should be updated to parse these new fields
- Registry entries may need updating to store the additional metadata

## Files to Modify

- `internal/notion/types.go` - Add Icon to Page struct
- `internal/converter/converter.go` - Update frontmatter generation
- `internal/sync/reindex.go` - Parse new frontmatter fields
- `internal/sync/state.go` - Consider storing additional metadata in registry

## Testing

1. Sync a mix of regular pages and database pages
2. Verify frontmatter contains all new fields
3. Test reindex correctly parses the new fields
4. Ensure backward compatibility with existing synced files
