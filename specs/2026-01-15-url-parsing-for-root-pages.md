# URL Parsing for Root Pages

**Date:** 2026-01-15
**Status:** Proposal

## Problem

Users currently need to provide only the Notion page ID when specifying root pages (e.g., `abc123def456`). However, Notion URLs are more accessible to users and easier to copy from their browser:

- Users often have the full URL: `https://www.notion.so/workspace/Page-Title-abc123def456`
- Extracting the ID manually is inconvenient and error-prone
- Different URL formats exist (with/without workspace, with/without page title)

## Goal

Allow users to provide full Notion URLs as root page identifiers and automatically extract the page ID:

```bash
# Currently required
notion-sync sync --root-page abc123def456

# Should also accept
notion-sync sync --root-page https://www.notion.so/abc123def456
notion-sync sync --root-page https://www.notion.so/workspace/Page-Title-abc123def456
notion-sync sync --root-page https://notion.so/workspace/abc123def456?pvs=4
```

## Notion URL Formats

Notion uses several URL patterns:

### Format 1: Direct ID
```
https://www.notion.so/abc123def456
https://notion.so/abc123def456
```

### Format 2: Workspace with ID
```
https://www.notion.so/workspace-name/abc123def456
```

### Format 3: Page Title with ID suffix
```
https://www.notion.so/Page-Title-abc123def456
https://www.notion.so/workspace/My-Page-Title-abc123def456
```

### Format 4: With query parameters
```
https://www.notion.so/abc123def456?pvs=4
https://www.notion.so/workspace/Page-Title-abc123def456?v=xyz&pvs=4
```

## ID Extraction Rules

1. **Strip query parameters**: Remove everything after `?`
2. **Remove protocol and domain**: Strip `https://`, `http://`, `www.notion.so/`, `notion.so/`
3. **Extract ID from path**:
   - If path contains only the ID (32 hex chars): use it directly
   - If path has multiple segments: take the last segment
   - If last segment contains hyphens: take the last hyphen-separated part (minimum 32 chars)
   - Remove any UUID dashes if present (convert `abc-123-def` to `abc123def`)

4. **Validate ID format**:
   - Must be exactly 32 hexadecimal characters (after normalization)
   - Valid chars: `0-9a-f` (case-insensitive)

## Implementation

### Function Signature

```go
// ParseNotionPageID extracts a Notion page ID from either a raw ID or a full URL
// Returns the normalized 32-character hex ID, or an error if invalid
func ParseNotionPageID(input string) (string, error)
```

### Examples

```go
ParseNotionPageID("abc123def456...")
// → "abc123def456..."

ParseNotionPageID("https://www.notion.so/abc123def456...")
// → "abc123def456..."

ParseNotionPageID("https://notion.so/workspace/Page-Title-abc123def456...")
// → "abc123def456..."

ParseNotionPageID("https://www.notion.so/abc-123-def-456...")
// → "abc123def456..."

ParseNotionPageID("https://notion.so/abc123def456?pvs=4")
// → "abc123def456..."

ParseNotionPageID("invalid")
// → error: "invalid notion page ID format"

ParseNotionPageID("https://example.com/page")
// → error: "URL is not a notion.so URL"
```

### Usage in Commands

Apply URL parsing to all commands that accept page IDs:

```bash
# sync command
notion-sync sync --root-page <url-or-id>

# Future commands that may accept page IDs
notion-sync export --page <url-or-id>
notion-sync download --page <url-or-id>
```

## Error Handling

When URL parsing fails, provide clear error messages:

```
Error: Invalid root page ID or URL: "https://example.com/page"
  - Must be a valid Notion page ID (32 hex characters)
  - Or a valid Notion URL (notion.so/...)

Examples:
  notion-sync sync --root-page abc123def456...
  notion-sync sync --root-page https://notion.so/abc123def456...
```

## Design Decisions

- **Flexible input**: Accept both IDs and URLs for user convenience
- **Transparent parsing**: Users shouldn't need to know about ID format
- **Strict validation**: Reject invalid inputs early with clear error messages
- **Query parameter handling**: Strip `?pvs=4` and other tracking parameters
- **Case-insensitive**: Accept both uppercase and lowercase hex characters
- **UUID format support**: Handle IDs with dashes (8-4-4-4-12 format) by removing them
- **Domain validation**: Only accept `notion.so` domain to prevent confusion

## Testing Considerations

Test cases should cover:
- Direct 32-character hex IDs
- URLs with www prefix
- URLs without www prefix
- URLs with workspace names
- URLs with page titles
- URLs with query parameters
- Mixed case hex characters
- UUID-formatted IDs with dashes
- Invalid inputs (wrong domain, malformed IDs, too short/long)
- Edge cases (empty string, just protocol, etc.)
