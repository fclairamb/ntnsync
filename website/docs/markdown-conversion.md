---
sidebar_position: 4
---

# Markdown Conversion

ntnsync converts Notion pages to GitHub-flavored Markdown with YAML frontmatter.

## Frontmatter

All markdown files begin with YAML frontmatter containing page metadata:

```yaml
---
ntnsync_version: 0.8.1
notion_id: 2c536f5e-48f4-4234-ad8d-73a1a148e95d
title: "Page Title"
notion_type: page
notion_folder: tech
file_path: tech/wiki/page.md
created_by: "Jane Doe <jane@example.com> [751deade]"
last_edited_by: "John Smith <john@example.com> [306d872b]"
last_edited: 2025-12-10T13:39:00Z
last_synced: 2026-01-18T18:05:06+01:00
icon: "emoji:📝"
notion_parent_id: abc123def456
is_root: false
notion_url: https://www.notion.so/2c536f5e48f44234ad8d73a1a148e95d
download_duration: 2.559393503s
---
```

| Field | Description |
|-------|-------------|
| `ntnsync_version` | Version of ntnsync that wrote the file |
| `notion_id` | Page ID with dashes |
| `title` | Page title (omitted when empty) |
| `notion_type` | `page` or `database` |
| `notion_folder` | Folder name |
| `file_path` | Relative path for self-reference |
| `created_by` | Page creator as `Name <email> [short-id]` (first 8 chars of the user ID) |
| `last_edited_by` | Last editor, same format as `created_by` |
| `last_edited` | Last edit timestamp from Notion |
| `last_synced` | Local sync timestamp |
| `icon` | Page icon: `emoji:📝`, `external:<url>`, or `file:<url>` (omitted when unset) |
| `notion_parent_id` | Parent page/database ID (omitted for root pages) |
| `is_root` | Whether this is a root page |
| `notion_url` | Notion web URL |
| `simplified_depth` | Only present when block discovery was depth-limited (`NTN_BLOCK_DEPTH`) |
| `download_duration` | How long the page took to download |

### Database Entry Properties

Pages whose parent is a database (database rows) additionally carry their
database properties under the `properties` key, sorted by property name.
Values are flattened to display values; properties with no value are omitted:

```yaml
properties:
  Priority: "High"
  Status: "In Progress"
  Tags:
    - "feature"
    - "urgent"
```

| Notion property type | Exported as |
|----------------------|-------------|
| `rich_text` | Plain text string |
| `number` | Number |
| `select`, `status` | Option name |
| `multi_select` | List of option names |
| `date` | Start date string |
| `checkbox` | `true` / `false` |
| `url`, `email`, `phone_number` | String |
| `people` | List of user IDs |
| `relation` | List of related page IDs |
| `formula`, `rollup` | Computed value (string, number, boolean, or date) |
| `created_by`, `last_edited_by` | User ID |
| `created_time`, `last_edited_time` | Timestamp |
| `unique_id` | `PREFIX-123` (or the bare number without a prefix) |
| `title` | Skipped — already exported as `title` |

## Block Type Conversions

### Text Blocks

**Paragraph**
```markdown
Regular paragraph text.
```

**Headings**
```markdown
# Heading 1
## Heading 2
### Heading 3
```

**Toggleable headings** include collapsible markers:
```markdown
# Toggleable Heading
<!-- collapsible: start -->
Content inside toggle
<!-- collapsible: end -->
```

### Lists

**Bulleted list**
```markdown
- Item 1
  - Nested item
    - Deeper nested
```

**Numbered list**
```markdown
1. First item
   1. Nested numbered
```

**To-do list**
```markdown
- [ ] Unchecked task
- [x] Checked task
```

### Toggle Blocks

```markdown
<!-- collapsible: start -->
**Toggle Title**

Content inside toggle
<!-- collapsible: end -->
```

### Code Blocks

````markdown
```python
def hello():
    print("world")
```
````

The language identifier comes from Notion's code block language setting.

### Quotes and Callouts

**Block quote**
```markdown
> This is a quote
> Multiple lines are prefixed
```

**Callout** (includes emoji)
```markdown
> [emoji] Callout content
> Multi-line callout content
```

### Media and Files

**Image**
```markdown
![alt text](https://url)<!-- file_id:abc123 -->
```

**File**
```markdown
[Document.pdf](https://url)<!-- file_id:abc123 -->
```

**Bookmark**
```markdown
[Bookmark](https://example.com)
```

**Embed**
```markdown
[Embed](https://example.com/embed)
```

### Tables

```markdown
| Column 1 | Column 2 |
| --- | --- |
| Cell 1 | Cell 2 |
```

### Divider

```markdown
---
```

### Equations

```markdown
$$
E = mc^2
$$
```

### Table of Contents

```markdown
[TOC]
```

## Page and Database Links

**Child page reference**
```markdown
- [Child Page](./parent-dir/child-page.md)<!-- page_id:abc123 -->
```

**Child database reference**
```markdown
- [Child Database](./parent-dir/db-name.md)<!-- page_id:abc123 -->
```

**Inline page link**
```markdown
[Page Link](notion://page/abc123def456)<!-- page_id:abc123def456 -->
```

**Inline database link**
```markdown
[Database Link](notion://database/xyz789)<!-- page_id:xyz789 -->
```

The `<!-- page_id:... -->` comment allows tools to track references even if filenames change.

## Database Content

Database pages display their child pages as a list:

```markdown
---
notion_type: database
...
---

# Database Title

Database description (if any)

- [Page 1](./database/page-1.md)<!-- page_id:abc -->
- [Page 2](./database/page-2.md)<!-- page_id:def -->
```

## Rich Text Formatting

All rich text fields support inline formatting:

| Notion Format | Markdown |
|---------------|----------|
| Bold | `**text**` |
| Italic | `_text_` |
| Strikethrough | `~~text~~` |
| Code | `` `text` `` |
| Link | `[text](url)` |

**Mentions** (users, dates, pages) are converted to plain text.

## ID Comments

To preserve references when filenames change, ntnsync adds HTML comments with IDs:

- **Files**: `<!-- file_id:abc123 -->`
- **Pages**: `<!-- page_id:abc123 -->`

These comments appear after links and images to enable tooling that needs to track references across renames.
