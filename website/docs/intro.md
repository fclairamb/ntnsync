---
sidebar_position: 1
---

# Introduction

ntnsync is a CLI tool that syncs Notion pages to local markdown files with git versioning.

## Features

- **Sync Notion to Markdown**: Convert your Notion pages to clean markdown files, preserving structure and formatting
- **Git Integration**: Automatic git commits and push support. Track changes to your documentation over time
- **Folder Organization**: Organize pages into logical folders
- **Incremental Updates**: Only sync pages that have changed since the last pull
- **Webhook Support**: Real-time sync with Notion webhooks
- **Stable File Paths**: File paths never change when pages are renamed in Notion

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

3. **Add a root page**

   ```bash
   ntnsync add https://www.notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d --folder tech
   ```

4. **Sync the queue**

   ```bash
   NTN_COMMIT=true ntnsync sync
   ```

5. **Pull updates later**

   ```bash
   ntnsync pull --since 24h
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
