# Root.md Task List Format

**Date:** 2026-01-31
**Status:** Draft

## Problem

The current `root.md` format uses a markdown table:

```markdown
| folder | enabled | url |
|--------|---------|-----|
| tech | [x] | https://notion.so/Wiki-abc123def456 |
| product | [ ] | https://notion.so/Specs-789012 |
```

This has several usability issues:

1. **No interactive checkboxes**: GitHub renders `[x]` inside table cells as plain text, not as clickable checkboxes
2. **Table alignment**: Adding/editing rows requires maintaining column alignment
3. **Poor readability**: Long URLs make the table visually cluttered
4. **Not mobile-friendly**: Tables are harder to edit on mobile devices

## Proposed Solution

Change to a GitHub task list format:

```markdown
# Root Pages

- [x] **tech**: https://notion.so/Wiki-abc123def456
- [ ] **product**: https://notion.so/Specs-789012
```

### Benefits

1. **Interactive checkboxes**: GitHub renders task list checkboxes as clickable UI elements - users can toggle enabled/disabled directly in the GitHub web interface
2. **Easy editing**: Just add a new line, no column alignment needed
3. **Better readability**: Each entry is on its own line with clear structure
4. **Mobile-friendly**: Simple list format works well on all devices
5. **Familiar format**: Developers already know task list syntax from issues/PRs

### Format Specification

Each entry follows this pattern:

```
- [x] **folder**: url
```

Where:
- `- [x]` or `- [ ]` - Task checkbox (enabled/disabled)
- `**folder**` - Bold folder name
- `:` - Separator
- `url` - Notion page URL

### Parsing Rules

1. Lines starting with `- [x]` or `- [ ]` are entries
2. The checkbox determines enabled state
3. Extract folder name between `**` markers
4. Everything after `: ` is the URL
5. Skip lines that don't match the pattern
6. Whitespace is trimmed

### Regex Pattern

```
^- \[([ xX])\] \*\*([^*]+)\*\*:\s*(.+)$
```

Groups:
1. Checkbox state (` ` or `x`/`X`)
2. Folder name
3. URL

### Full Example

```markdown
# Root Pages

- [x] **tech**: https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d
- [x] **product**: https://notion.so/Product-Specs-abc123def456
- [ ] **archive**: https://notion.so/Old-Docs-disabled123
```

### Empty Template

When `root.md` is created, start with:

```markdown
# Root Pages

```

No table header needed - users just add lines.

## Implementation

### Files to Modify

- `internal/sync/rootmd.go`:
  - Replace `parseRootMdContent()` with task list parser
  - Update `formatRootMd()` to output task list format
  - Update `rootMdTemplate` constant

### Parser Function

```go
func parseRootMdTaskList(data []byte) (*RootManifest, error) {
    manifest := &RootManifest{}
    scanner := bufio.NewScanner(bytes.NewReader(data))

    // Pattern: - [x] **folder**: url
    pattern := regexp.MustCompile(`^- \[([ xX])\] \*\*([^*]+)\*\*:\s*(.+)$`)

    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        matches := pattern.FindStringSubmatch(line)
        if matches == nil {
            continue
        }

        enabled := matches[1] == "x" || matches[1] == "X"
        folder := matches[2]
        url := matches[3]

        pageID, err := notion.ParsePageIDOrURL(url)
        if err != nil {
            continue // Skip invalid URLs
        }

        manifest.Entries = append(manifest.Entries, RootEntry{
            Folder:  folder,
            Enabled: enabled,
            URL:     url,
            PageID:  pageID,
        })
    }

    return manifest, scanner.Err()
}
```

### Updated Format Function

```go
func formatRootMd(manifest *RootManifest) string {
    var buf bytes.Buffer
    buf.WriteString("# Root Pages\n\n")

    for _, entry := range manifest.Entries {
        checkbox := "[ ]"
        if entry.Enabled {
            checkbox = "[x]"
        }
        buf.WriteString(fmt.Sprintf("- %s **%s**: %s\n", checkbox, entry.Folder, entry.URL))
    }

    return buf.String()
}
```

### Updated Template

```go
const rootMdTemplate = `# Root Pages

`
```

## Testing

### Unit Tests

1. Parse task list format with enabled entries
2. Parse task list format with disabled entries
3. Parse mixed enabled/disabled
4. Parse empty file (just header)
5. Parse with extra whitespace
6. Format manifest to task list
7. Round-trip: parse then format

### Integration Tests

1. GitHub checkbox toggle works (manual verification)
2. Duplicate detection still works
3. Reconciliation creates task list format

## Documentation Updates

Update these files:
- `docs/cli-commands.md` - Update root.md format examples
- `CLAUDE.md` - Update quick reference example

## Rollout

1. Implement new parser
2. Update formatter to output new format
3. Update documentation
