# Claude Code Guidelines

## Documentation

See `docs/` for detailed documentation:
- [CLI Commands](docs/cli-commands.md) - All commands with flags and examples
- [File Architecture](docs/file-architecture.md) - Directory structure, registries, queue system
- [Markdown Conversion](docs/markdown-conversion.md) - How Notion blocks become markdown
- [Development](docs/development.md) - Logging, code organization, building

## Quick Reference

**Root pages are configured in `root.md`**:
```markdown
# Root Pages

| folder | enabled | url |
|--------|---------|-----|
| tech | [x] | https://notion.so/Wiki-abc123 |
| product | [ ] | https://notion.so/Specs-def456 |
```

**Common commands**:
```bash
./ntnsync pull --since 24h                      # Queue changed pages
./ntnsync sync                                  # Process queue
NTN_COMMIT=true ./ntnsync sync                  # Process queue, commit
./ntnsync list --tree                           # Show page hierarchy
./ntnsync cleanup --dry-run                     # Preview orphaned pages
```

**Commit/Push environment variables**:
- `NTN_COMMIT=true` - Enable automatic git commit after changes
- `NTN_COMMIT_PERIOD=1m` - Commit periodically during sync (e.g., every 1 minute)
- `NTN_PUSH=true/false` - Push to remote (defaults to true when `NTN_GIT_URL` is set)

**Logging environment variables**:
- `NTN_LOG_FORMAT=text|json` - Log format (default: text, use json for CI/CD)

**Performance environment variables**:
- `NTN_BLOCK_DEPTH=N` - Limit block discovery depth (default: 0 = unlimited)

**Key concepts**:
- File paths never change when pages are renamed
- Queue types: `init` (skip if exists) vs `update` (always process)
- Databases are pages with `notion_type: database`

**Plans**: Write plan files to `specs/YYYY-MM-DD-<name>.md`
