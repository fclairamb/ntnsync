# Block Discovery Depth Limit

**Date**: 2026-01-29
**Status**: Implemented

## Overview

Add a configurable maximum depth limit for page block discovery. When the limit is reached, stop exploring deeper nested blocks and mark the page as "simplified" in its frontmatter.

## Motivation

Some Notion pages have extremely deep block hierarchies (nested toggles, callouts, columns, etc.) that can:
- Cause slow sync times due to excessive API calls
- Generate very large markdown files that are hard to navigate
- Hit API rate limits when crawling deeply nested content
- Contain deeply nested content that is rarely needed in the synced output

A depth limit allows users to control how much nested content is synced, trading completeness for performance.

## Design

### Environment Variable

| Variable | Description | Default |
|----------|-------------|---------|
| `NTN_BLOCK_DEPTH` | Maximum depth for block discovery | `0` (unlimited) |

#### `NTN_BLOCK_DEPTH`

**Valid values**:
- `0` - Unlimited depth (default behavior, fetch all nested blocks)
- Positive integer - Maximum depth level to explore (e.g., `3`)

**Depth counting**:
- Depth `0` (initial call) = direct children of the page
- Depth `1` = children of those blocks
- Depth `2` = grandchildren, etc.

When `NTN_BLOCK_DEPTH=3`, blocks at depth 0, 1, 2, and 3 are fetched. Blocks at depth 4+ are not explored.

### Behavior

When the depth limit is reached:
1. Stop recursive block discovery at that level
2. Log an info message indicating depth limit was reached
3. Continue processing the fetched blocks normally
4. Add `simplified_depth` to the page frontmatter

### Frontmatter Changes

Add a new optional field to frontmatter when a page was depth-limited:

```yaml
---
notion_id: abc123
title: "My Page"
notion_type: page
simplified_depth: 3           # The depth limit that was used
# ... other fields
---
```

**Field definition**:
- `simplified_depth: N` - The depth limit that was active when this page was synced. Presence of this field indicates the page did not receive full block exploration.

This field is:
- **Added** when `NTN_BLOCK_DEPTH > 0` and blocks exist beyond the limit
- **Removed** when re-syncing with `NTN_BLOCK_DEPTH=0` (unlimited)
- **Updated** when re-syncing with a different depth limit

### Affected Operations

The depth limit applies to:
- `sync` command (queue processing)
- `add` command (initial page exploration)
- Any operation calling `GetAllBlockChildren()`

## Implementation

### Modify GetAllBlockChildren

Location: `/internal/notion/blocks.go`

```go
// GetAllBlockChildren fetches all block children recursively.
// If maxDepth > 0, recursion stops at that depth level.
func (c *Client) GetAllBlockChildren(ctx context.Context, blockID string, depth int, maxDepth int) ([]Block, error) {
    logArgs := []any{"block_id", blockID, "depth", depth}
    if maxDepth > 0 {
        logArgs = append(logArgs, "max_depth", maxDepth)
    }
    slog.DebugContext(ctx, "Fetching all block children", logArgs...)

    // ... existing pagination logic ...

    for i := range allBlocks {
        block := &allBlocks[i]
        if block.HasChildren {
            // Check depth limit before recursing
            if maxDepth > 0 && depth >= maxDepth {
                slog.InfoContext(ctx, "Depth limit reached, skipping children",
                    "block_id", block.ID,
                    "block_type", block.Type,
                    "depth", depth,
                    "max_depth", maxDepth,
                )
                continue
            }

            children, err := c.GetAllBlockChildren(ctx, block.ID, depth+1, maxDepth)
            if err != nil {
                // ... existing error handling ...
            }
            block.Children = children
        }
    }

    return allBlocks, nil
}
```

### Parse Environment Variable

Location: `/internal/sync/process.go` (or new file `/internal/sync/config.go`)

```go
func getBlockDepthLimit() int {
    val := os.Getenv("NTN_BLOCK_DEPTH")
    if val == "" || val == "0" {
        return 0 // Unlimited
    }

    depth, err := strconv.Atoi(val)
    if err != nil || depth < 0 {
        slog.Warn("Invalid NTN_BLOCK_DEPTH value, using unlimited",
            "value", val,
        )
        return 0
    }

    return depth
}
```

### Track if Depth Limit Was Hit

The `GetAllBlockChildren` function needs to return whether depth limiting occurred:

```go
type BlockFetchResult struct {
    Blocks     []Block
    WasLimited bool  // True if any children were skipped due to depth limit
    MaxDepth   int   // The limit that was applied (0 if unlimited)
}

func (c *Client) GetAllBlockChildrenWithLimit(ctx context.Context, blockID string, maxDepth int) (BlockFetchResult, error) {
    wasLimited := false

    var fetchRecursive func(blockID string, depth int) ([]Block, error)
    fetchRecursive = func(blockID string, depth int) ([]Block, error) {
        // ... fetch blocks ...

        for i := range blocks {
            block := &blocks[i]
            if block.HasChildren {
                if maxDepth > 0 && depth >= maxDepth {
                    wasLimited = true
                    continue
                }
                children, err := fetchRecursive(block.ID, depth+1)
                // ...
            }
        }
        return blocks, nil
    }

    blocks, err := fetchRecursive(blockID, 0)
    return BlockFetchResult{
        Blocks:     blocks,
        WasLimited: wasLimited,
        MaxDepth:   maxDepth,
    }, err
}
```

### Update ConvertOptions

Location: `/internal/converter/converter.go`

```go
type ConvertOptions struct {
    // ... existing fields ...
    SimplifiedDepth int   // The depth limit used (0 if not limited)
}
```

### Update Frontmatter Generation

Location: `/internal/converter/converter.go` in `generateFrontmatter()`

```go
func generateFrontmatter(page *notion.Page, opts ConvertOptions) string {
    // ... existing fields ...

    if opts.SimplifiedDepth > 0 {
        builder.WriteString(fmt.Sprintf("simplified_depth: %d\n", opts.SimplifiedDepth))
    }

    // ... rest of function ...
}
```

### Update processPage

Location: `/internal/sync/process.go`

```go
func (c *Crawler) processPage(ctx context.Context, pageID, folder string) error {
    // ... existing code ...

    maxDepth := getBlockDepthLimit()
    result, err := c.notion.GetAllBlockChildrenWithLimit(ctx, pageID, maxDepth)
    if err != nil {
        return err
    }

    // Only set SimplifiedDepth if limiting actually occurred
    simplifiedDepth := 0
    if result.WasLimited {
        simplifiedDepth = result.MaxDepth
    }

    content := c.converter.ConvertWithOptions(page, result.Blocks, converter.ConvertOptions{
        // ... existing options ...
        SimplifiedDepth: simplifiedDepth,
    })

    // ... rest of function ...
}
```

## Examples

### Default behavior (unlimited)

```bash
./ntnsync sync

# All nested blocks are fetched, no simplified_depth in frontmatter
```

### Limit to 3 levels deep

```bash
NTN_BLOCK_DEPTH=3 ./ntnsync sync

# Output:
# INFO Depth limit reached, skipping children block_id=abc depth=3 max_depth=3
# INFO Synced page page_id=xyz simplified_depth=3
```

Resulting frontmatter:
```yaml
---
notion_id: xyz
title: "Deeply Nested Page"
simplified_depth: 3
---
```

### In CI/CD for faster syncs

```yaml
jobs:
  sync:
    runs-on: ubuntu-latest
    env:
      NTN_BLOCK_DEPTH: 5  # Limit depth for faster CI
    steps:
      - run: ./ntnsync sync
```

### Re-sync without limit to get full content

```bash
# Previous sync was limited
NTN_BLOCK_DEPTH=0 ./ntnsync sync

# Page is re-synced with full depth, simplified_depth is removed from frontmatter
```

## Success Criteria

- [x] `NTN_BLOCK_DEPTH` environment variable is parsed correctly
- [x] Default is 0 (unlimited) when variable is unset
- [x] Depth limit stops block recursion at the specified level
- [x] `simplified_depth: N` is added to frontmatter when depth limiting occurs
- [x] `simplified_depth` is removed when re-syncing without limit
- [x] Info-level log message when depth limit is hit
- [x] Invalid values (negative, non-numeric) fall back to unlimited (silently)
- [x] Documentation updated (CLAUDE.md, docs/cli-commands.md)
