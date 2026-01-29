package sync

import (
	"context"
	"fmt"
	"os"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// CleanupResult contains the result of a cleanup operation.
type CleanupResult struct {
	OrphanedPages     int
	DeletedRegistries int
	DeletedFiles      int
}

// Cleanup deletes orphaned pages that don't trace back to a root in root.md.
func (c *Crawler) Cleanup(ctx context.Context, dryRun bool) (*CleanupResult, error) {
	c.logger.InfoContext(ctx, "starting cleanup", "dry_run", dryRun)

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return nil, fmt.Errorf("ensure transaction: %w", err)
	}

	// Get valid root IDs from root.md
	rootIDs, err := c.GetRootPageIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("get root page IDs: %w", err)
	}

	c.logger.InfoContext(ctx, "found root pages in root.md", "count", len(rootIDs))

	// List all page registries
	registries, err := c.listPageRegistries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}

	c.logger.InfoContext(ctx, "found page registries", "count", len(registries))

	result := &CleanupResult{}

	// Check each registry
	for _, reg := range registries {
		// Trace to root
		rootID, err := c.traceToRoot(ctx, reg.ID)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to trace to root",
				"page_id", reg.ID,
				"error", err)
			continue
		}

		// Check if root is in root.md
		if rootID != "" && rootIDs[rootID] {
			// This page traces to a valid root
			continue
		}

		// Orphaned page
		result.OrphanedPages++
		c.logger.InfoContext(ctx, "found orphaned page",
			"page_id", reg.ID,
			"title", reg.Title,
			"file_path", reg.FilePath,
			"root_id", rootID,
			"dry_run", dryRun)

		if dryRun {
			continue
		}

		// Delete the markdown file
		if reg.FilePath != "" {
			if err := c.deleteFile(ctx, reg.FilePath); err != nil {
				c.logger.WarnContext(ctx, "failed to delete markdown file",
					"file_path", reg.FilePath,
					"error", err)
			} else {
				result.DeletedFiles++
			}
		}

		// Delete the registry file
		if err := c.deletePageRegistry(ctx, reg.ID); err != nil {
			c.logger.WarnContext(ctx, "failed to delete registry",
				"page_id", reg.ID,
				"error", err)
		} else {
			result.DeletedRegistries++
		}
	}

	c.logger.InfoContext(ctx, "cleanup complete",
		"orphaned_pages", result.OrphanedPages,
		"deleted_registries", result.DeletedRegistries,
		"deleted_files", result.DeletedFiles,
		"dry_run", dryRun)

	return result, nil
}

// traceToRoot traces from a page up to its root and returns the root page ID.
// Returns empty string if no root is found (orphaned).
func (c *Crawler) traceToRoot(ctx context.Context, pageID string) (string, error) {
	visited := make(map[string]bool)
	currentID := pageID

	for {
		if visited[currentID] {
			return "", fmt.Errorf("%w: at page %s", apperrors.ErrCycleDetected, currentID)
		}
		visited[currentID] = true

		reg, err := c.loadPageRegistry(ctx, currentID)
		if err != nil {
			// No registry - orphaned
			return "", nil //nolint:nilerr // not finding registry is not an error, just means orphaned
		}
		if reg == nil {
			return "", nil
		}

		if reg.IsRoot {
			return currentID, nil
		}

		if reg.ParentID == "" {
			// No parent and not a root - orphaned
			return "", nil
		}
		currentID = reg.ParentID
	}
}

// deleteFile deletes a file from the store.
func (c *Crawler) deleteFile(ctx context.Context, filePath string) error {
	if err := c.tx.Delete(ctx, filePath); err != nil {
		// Check if file doesn't exist (not an error)
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete file %s: %w", filePath, err)
	}
	return nil
}

// deletePageRegistry deletes a page registry file.
func (c *Crawler) deletePageRegistry(ctx context.Context, pageID string) error {
	path := fmt.Sprintf("%s/%s/page-%s.json", stateDir, idsDir, pageID)
	if err := c.tx.Delete(ctx, path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete registry: %w", err)
	}
	return nil
}
