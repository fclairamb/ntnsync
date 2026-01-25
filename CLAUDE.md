# Claude Code Guidelines

## Documentation

See `docs/` for detailed documentation:
- [CLI Commands](docs/cli-commands.md) - All commands with flags and examples
- [File Architecture](docs/file-architecture.md) - Directory structure, registries, queue system
- [Markdown Conversion](docs/markdown-conversion.md) - How Notion blocks become markdown
- [Development](docs/development.md) - Logging, code organization, building

## Quick Reference

**Common commands**:
```bash
./ntnsync add <page_id_or_url> --folder tech   # Add root page
./ntnsync pull --since 24h                      # Queue changed pages
./ntnsync sync                                  # Process queue
NTN_COMMIT=true ./ntnsync sync                  # Process queue, commit
./ntnsync list --tree                           # Show page hierarchy
```

**Commit/Push environment variables**:
- `NTN_COMMIT=true` - Enable automatic git commit after changes
- `NTN_COMMIT_PERIOD=1m` - Commit periodically during sync (e.g., every 1 minute)
- `NTN_PUSH=true/false` - Push to remote (defaults to true when `NTN_GIT_URL` is set)

**Key concepts**:
- File paths never change when pages are renamed
- Queue types: `init` (skip if exists) vs `update` (always process)
- Databases are pages with `notion_type: database`

**Plans**: Write plan files to `specs/YYYY-MM-DD-<name>.md`
