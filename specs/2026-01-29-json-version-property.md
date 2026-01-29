# JSON Version Property

## Summary

Add an `ntnsync_version` property to all JSON files to track which version of ntnsync created them.

## Motivation

- Enable future migrations when JSON schema changes
- Help debugging by knowing which version generated a file
- Allow backward compatibility checks when reading files

## Implementation

### Affected Files

All files written by ntnsync should include the version property:

**JSON files:**
- `.notion-sync/ids/page-$pageid.json` - Page ID mapping files
- `.notion-sync/ids/file-$fileid.json` - File ID mapping files
- `.notion-sync/state.json` - Sync state file

**Markdown files (in frontmatter):**
- `**/$page.md` - Page content files
- `**/$page/files/$file.meta.json` - File metadata JSON

### Schema Change

**JSON files** - Add a top-level property:

```json
{
  "ntnsync_version": "0.4.0",
  ...existing properties...
}
```

**Markdown files** - Add to frontmatter:

```yaml
---
ntnsync_version: "0.4.0"
...existing frontmatter...
---
```

### Version Source

The version should come from the build-time version variable (already used for `--version` flag).

### Behavior

- **Write**: Always include `ntnsync_version` when writing JSON files
- **Read**: Accept files with or without the property (backward compatible)
- **No migration**: Initially, just record the version without triggering any migration logic

## Future Considerations

- Add migration logic when schema changes require it
- Potentially warn when reading files from a newer version
