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

// GetAllBlockChildren retrieves all children of a block recursively.
func (c *Client) GetAllBlockChildren(ctx context.Context, blockID string, depth int) ([]Block, error) {
	// Store pageId in context on first call (when blockID is the page itself)
	if PageIDFromContext(ctx) == "" {
		ctx = WithPageID(ctx, blockID)
	}

	logArgs := []any{"block_id", blockID, "depth", depth}
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
				children, err := c.GetAllBlockChildren(ctx, block.ID, depth+1)
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
