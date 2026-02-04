package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/notion"
)

// computeParentDir computes the directory for a child item based on its parent.
func (c *Crawler) computeParentDir(ctx context.Context, parentID, defaultFolder string) string {
	if parentID == "" {
		return defaultFolder
	}

	parentReg, err := c.loadPageRegistry(ctx, parentID)
	if err != nil {
		// Parent not found - use folder root
		return defaultFolder
	}

	// Place in parent's directory
	parentDir := filepath.Dir(parentReg.FilePath)
	parentBase := strings.TrimSuffix(filepath.Base(parentReg.FilePath), ".md")
	return filepath.Join(parentDir, parentBase)
}

// computeFilePath determines the file path for a page or database.
// CRITICAL: Enforces file path stability - existing paths are never changed.
// resolvedParentID is the parent page/database ID (already resolved from blocks).
func (c *Crawler) computeFilePath(
	ctx context.Context, page *notion.Page, folder string, isRoot bool, resolvedParentID string,
) string {
	pageID := normalizePageID(page.ID)

	// Check page registry for existing path (stability)
	if reg, err := c.loadPageRegistry(ctx, pageID); err == nil && reg.FilePath != "" {
		c.logger.DebugContext(ctx, "using registry path for stability",
			"page_id", pageID,
			"path", reg.FilePath)
		return reg.FilePath
	}

	// Compute new path for new page
	title := converter.SanitizeFilename(page.Title())
	if title == "" {
		title = defaultUntitledStr
	}

	var dir string

	if isRoot {
		// Root page: $folder/$title.md
		dir = folder
	} else {
		// Child page: $folder/$parent-dir/$title.md
		dir = c.computeParentDir(ctx, resolvedParentID, folder)
	}
	filename := title

	// Check for conflicts and add short ID if needed
	filename = c.resolveFilenameConflict(ctx, folder, dir, filename, pageID)

	return filepath.Join(dir, filename+".md")
}

// resolveFilenameConflict checks for filename conflicts and adds ID suffix if needed.
func (c *Crawler) resolveFilenameConflict(ctx context.Context, _, dir, baseFilename, pageID string) string {
	// List all page registries to find conflicts
	registries, err := c.listPageRegistries(ctx)
	if err != nil {
		return baseFilename
	}

	usedNames := make(map[string]string) // lowercase filename -> pageID

	for _, reg := range registries {
		if reg.ID == pageID {
			continue // Skip self
		}
		regDir := filepath.Dir(reg.FilePath)
		if regDir == dir {
			name := strings.TrimSuffix(filepath.Base(reg.FilePath), ".md")
			usedNames[strings.ToLower(name)] = reg.ID
		}
	}

	// Check if base filename is available
	lowerBase := strings.ToLower(baseFilename)
	if _, exists := usedNames[lowerBase]; !exists {
		return baseFilename
	}

	// Conflict - add 4-char ID suffix
	shortID := pageID
	if len(shortID) > shortIDLength {
		shortID = shortID[:shortIDLength]
	}
	return fmt.Sprintf("%s-%s", baseFilename, shortID)
}
