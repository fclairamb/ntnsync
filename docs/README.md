# ntnsync Documentation

ntnsync synchronizes Notion pages to a local git repository as markdown files.

## Contents

| Document | Description |
|----------|-------------|
| [CLI Commands](cli-commands.md) | All commands with flags, examples, and workflows |
| [File Architecture](file-architecture.md) | Directory structure, state files, registries, queue system |
| [Markdown Conversion](markdown-conversion.md) | How Notion blocks are converted to markdown |
| [Development](development.md) | Logging guidelines, code organization, building |

## Overview

ntnsync uses a **folder-based organization** where pages are grouped into named folders (e.g., `tech`, `product`). Each folder contains root pages and their nested children.

**Sync workflow**:
1. `add` - Add a root page to a folder
2. `pull` - Queue pages that changed since last pull
3. `sync` - Process the queue, download pages, write markdown

**Key features**:
- Stable file paths (don't change on rename)
- Git-backed storage with atomic commits
- Incremental sync with change detection
- Database support with child page listings
