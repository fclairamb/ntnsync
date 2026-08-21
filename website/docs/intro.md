---
sidebar_position: 1
---

import Pipeline from "@site/static/img/pipeline.svg";
import EverythingIsAPage from "@site/static/img/everything-is-a-page.svg";
import PropertiesFrontmatter from "@site/static/img/properties-frontmatter.svg";

# Introduction

**ntnsync mirrors a Notion workspace into plain markdown files in a git repository, continuously.**
Every page &mdash; and every database, and every row of every database &mdash; becomes one `.md`
file whose path never changes, committed on every sync.

<div className="ntnDiagram">
  <Pipeline
    role="img"
    aria-label="Pipeline: Notion (hosted) holds pages, databases and their block tree. ntnsync converts blocks to markdown and properties to frontmatter. The result is *.md files with stable paths plus attachments, which you own. Those files go into git, one commit per sync. A dashed arrow returning from git to Notion is labelled: nothing is ever written back, ntnsync only reads Notion."
  />
</div>

## Why you would run this

1. **Your LLM can actually read it.** The Notion API makes an agent paginate a block tree, one
   page at a time. A markdown tree is grep-able and chunk-able, so the whole workspace fits into
   a coding agent's context the same way your source code does.
2. **History with no expiry date.** Notion's page history is capped by your plan. Git's is not
   capped at all. Every sync is a commit, so *"what did this spec say in March?"* is a `git log`
   away &mdash; forever.
3. **No lock-in, no cliff edge.** If the subscription lapses or the team moves tools, nothing has
   to be rescued. The knowledge is already on disk, in the one format every editor, search tool
   and model already reads.

## "But Notion already exports markdown"

It does &mdash; once, by hand, into a fresh folder with fresh filenames. ntnsync differs in three
ways, and they are the whole point:

- **It runs continuously.** `ntnsync pull` queues the pages that changed since the last run,
  `ntnsync sync` processes the queue. Put it on a schedule or wire it to a Notion webhook and the
  mirror keeps itself current.
- **File paths are stable.** Renaming a page in Notion does not move its file. A rename shows up
  as a one-line change to the title, not as a delete plus an add somewhere else in the tree.
- **Every version lands in git.** Because the paths hold still, the diffs actually mean something:
  you can follow a single document through its whole life with `git log --follow`.

:::note[One-way, and read-only]

ntnsync reads Notion and writes files. It **never writes back to Notion** &mdash; not a page, not
a property, not a comment. Editing the generated markdown changes nothing in your workspace, and
the next sync will overwrite it.

So this is a *mirror*, not a backup you can restore into. What you get is readable, searchable,
permanently versioned markdown &mdash; not a workspace in a box.

:::

## Everything is a page

Notion has several concepts that behave differently in its UI: a page, a database, and a row
inside a database. ntnsync flattens all three into the same thing on disk &mdash; a markdown file
with frontmatter. The `notion_type` field records which one it came from.

<div className="ntnDiagram">
  <EverythingIsAPage
    role="img"
    aria-label="Two columns, in Notion and on disk. A page named Engineering Wiki becomes tech/engineering-wiki.md with notion_type page. A database named Releases becomes releases/releases.md with notion_type database. A row inside that database, drawn nested with a dashed border, becomes releases/client-platform-v0740.md with notion_type page plus a properties block. Three different Notion concepts, one shape on disk: a markdown file."
  />
</div>

A database file lists its rows; each row is its own file next to it. Nothing needs a special
reader &mdash; it is markdown all the way down.

## Properties become frontmatter

A database row carries properties. ntnsync copies them into YAML frontmatter under a `properties:`
key, **verbatim**: the property name you see in Notion is the key you get in the file, spaces and
capitalisation included. List-shaped values (relations, multi-selects, people) nest as a YAML
sequence under their own key; everything else stays flat.

<div className="ntnDiagram">
  <PropertiesFrontmatter
    role="img"
    aria-label="A Notion properties panel on the left, with Autoprod unchecked, Component set to client-platform, Issues holding one linked row, and Last edited time set to August 17 2026 at 11:42. On the right, the YAML frontmatter it becomes: a properties key, then Autoprod false, Component client-platform, an Issues key whose single UUID value is nested as a YAML sequence item, and a Last edited time key. Property names are copied verbatim, spaces and casing included; list-shaped values nest as a sequence, everything else stays flat."
  />
</div>

Verbatim names are a deliberate trade: the file reads like the Notion page rather than like a
normalised export, so a human (or a model) reading the file recognises the workspace it came from.

## Stable file paths

This is the feature the rest depends on. A file is named once, from the page's identity, and keeps
that name for good:

- Rename a page in Notion and the file stays where it is.
- Move a page and its file stays where it is.
- Diffs therefore describe content changes, not filesystem churn.

Stable paths are what make the git history usable, and a usable history is what lets a model (or
you) follow a document through time.

## Installation

### From Source

```bash
go install github.com/fclairamb/ntnsync@latest
```

### Docker

```bash
docker pull ghcr.io/fclairamb/ntnsync:latest
```

## Quick Start

1. **Get a Notion API Token**

   Create an integration at [https://www.notion.so/my-integrations](https://www.notion.so/my-integrations) and copy the token.

2. **Set the token**

   ```bash
   export NOTION_TOKEN=secret_xxx
   ```

3. **Create root.md with your root pages**

   ```bash
   cat > notion/root.md << 'EOF'
   # Root Pages

   - [x] **tech**: https://www.notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d
   EOF
   ```

4. **Pull pages to queue (use --since for first pull)**

   ```bash
   ntnsync pull --since 30d
   ```

5. **Sync the queue**

   ```bash
   NTN_COMMIT=true ntnsync sync
   ```

6. **Pull updates later**

   ```bash
   ntnsync pull
   NTN_COMMIT=true ntnsync sync
   ```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `NOTION_TOKEN` | Yes | Notion integration token |
| `NTN_DIR` | No | Storage directory (default: `notion`) |
| `NTN_COMMIT` | No | Enable automatic git commit |
| `NTN_PUSH` | No | Push to remote after commits |
| `NTN_GIT_URL` | No | Remote git repository URL |
| `NTN_GIT_PASS` | No | Git password/token |
