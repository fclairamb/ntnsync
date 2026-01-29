package notion

import (
	"context"
	"fmt"
)

// GetBlock retrieves a block by ID.
func (c *Client) GetBlock(ctx context.Context, blockID string) (*Block, error) {
	path := "/blocks/" + blockID

	var block Block
	if err := c.do(ctx, "GET", path, nil, &block); err != nil {
		return nil, fmt.Errorf("get block %s: %w", blockID, err)
	}

	return &block, nil
}

// GetBlockChildren retrieves children of a block with pagination.
func (c *Client) GetBlockChildren(ctx context.Context, blockID string, cursor string) (*BlockChildrenResponse, error) {
	path := fmt.Sprintf("/blocks/%s/children?page_size=100", blockID)
	if cursor != "" {
		path += "&start_cursor=" + cursor
	}

	var result BlockChildrenResponse
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, fmt.Errorf("get block children %s: %w", blockID, err)
	}

	return &result, nil
}

// BlockFetchResult holds the result of fetching blocks with depth limiting.
type BlockFetchResult struct {
	Blocks     []Block
	WasLimited bool // True if any children were skipped due to depth limit
	MaxDepth   int  // The limit that was applied (0 if unlimited)
}

// GetAllBlockChildren retrieves all children of a block recursively.
// The depth parameter is kept for backward compatibility but is no longer used.
func (c *Client) GetAllBlockChildren(ctx context.Context, blockID string, _ int) ([]Block, error) {
	result, err := c.GetAllBlockChildrenWithLimit(ctx, blockID, 0)
	if err != nil {
		return nil, err
	}
	return result.Blocks, nil
}

// GetAllBlockChildrenWithLimit retrieves all children of a block recursively with an optional depth limit.
// If maxDepth > 0, recursion stops at that depth level.
//
//nolint:gocognit,nestif,funlen // Recursive block fetching with depth limiting requires nested logic
func (c *Client) GetAllBlockChildrenWithLimit(
	ctx context.Context, blockID string, maxDepth int,
) (BlockFetchResult, error) {
	wasLimited := false

	var fetchRecursive func(blockID string, depth int) ([]Block, error)
	fetchRecursive = func(blockID string, depth int) ([]Block, error) {
		// Store pageId in context on first call (when blockID is the page itself)
		if depth == 0 && PageIDFromContext(ctx) == "" {
			ctx = WithPageID(ctx, blockID)
		}

		logArgs := []any{"block_id", blockID, "depth", depth}
		if maxDepth > 0 {
			logArgs = append(logArgs, "max_depth", maxDepth)
		}
		if pageID := PageIDFromContext(ctx); pageID != "" {
			logArgs = append(logArgs, "page_id", pageID)
		}
		c.logger.DebugContext(ctx, "fetching all block children", logArgs...)

		var allBlocks []Block
		var cursor string

		for {
			result, err := c.GetBlockChildren(ctx, blockID, cursor)
			if err != nil {
				return nil, err
			}

			// Recursively fetch children for blocks that have them
			for i := range result.Results {
				block := &result.Results[i]
				if block.HasChildren {
					// Check depth limit before recursing
					if maxDepth > 0 && depth >= maxDepth {
						wasLimited = true
						infoArgs := []any{
							"block_id", block.ID,
							"block_type", block.Type,
							"depth", depth,
							"max_depth", maxDepth,
						}
						if pageID := PageIDFromContext(ctx); pageID != "" {
							infoArgs = append(infoArgs, "page_id", pageID)
						}
						c.logger.InfoContext(ctx, "depth limit reached, skipping children", infoArgs...)
					} else {
						children, err := fetchRecursive(block.ID, depth+1)
						if err != nil {
							warnArgs := []any{"block_id", block.ID, "depth", depth + 1, "error", err}
							if pageID := PageIDFromContext(ctx); pageID != "" {
								warnArgs = append(warnArgs, "page_id", pageID)
							}
							c.logger.WarnContext(ctx, "failed to get block children", warnArgs...)
							// Continue without children rather than failing
						} else {
							block.Children = children
						}
					}
				}
				allBlocks = append(allBlocks, *block)
			}

			if !result.HasMore || result.NextCursor == nil {
				break
			}
			cursor = *result.NextCursor
		}

		doneLogArgs := []any{"block_id", blockID, "depth", depth, "count", len(allBlocks)}
		if pageID := PageIDFromContext(ctx); pageID != "" {
			doneLogArgs = append(doneLogArgs, "page_id", pageID)
		}
		c.logger.DebugContext(ctx, "fetched all block children", doneLogArgs...)
		return allBlocks, nil
	}

	blocks, err := fetchRecursive(blockID, 0)
	if err != nil {
		return BlockFetchResult{}, err
	}

	return BlockFetchResult{
		Blocks:     blocks,
		WasLimited: wasLimited,
		MaxDepth:   maxDepth,
	}, nil
}
