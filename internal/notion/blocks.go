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
func (c *Client) GetAllBlockChildren(ctx context.Context, blockID string) ([]Block, error) {
	c.logger.DebugContext(ctx, "fetching all block children", "block_id", blockID)

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
				children, err := c.GetAllBlockChildren(ctx, block.ID)
				if err != nil {
					c.logger.WarnContext(ctx, "failed to get block children",
						"block_id", block.ID,
						"error", err)
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

	c.logger.DebugContext(ctx, "fetched all block children", "block_id", blockID, "count", len(allBlocks))
	return allBlocks, nil
}
