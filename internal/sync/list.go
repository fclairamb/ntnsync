package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/ntnsync/internal/queue"
)

// PageInfo contains displayable information about a page.
type PageInfo struct {
	ID         string
	Title      string
	Path       string
	LastSynced time.Time
	IsRoot     bool
	IsOrphaned bool
	ParentID   string
	Children   []*PageInfo
}

// FolderInfo contains information about a folder.
type FolderInfo struct {
	Name          string
	RootPages     int
	TotalPages    int
	OrphanedPages int
	Pages         []*PageInfo
}

// QueueInfo contains information about queue entries.
type QueueInfo struct {
	Folder    string
	Type      string
	PageCount int
	QueueFile string
}

// StatusInfo contains sync status information.
type StatusInfo struct {
	FolderCount    int
	TotalPages     int
	TotalRootPages int
	QueueEntries   []*QueueInfo
	Folders        map[string]*FolderStatus
}

// FolderStatus contains status for a specific folder.
type FolderStatus struct {
	Name        string
	PageCount   int
	RootPages   int
	LastSynced  *time.Time
	QueuedPages int
}

// ListPages returns page information for display.
//
//nolint:funlen,gocognit // Complex tree building and sorting logic
func (c *Crawler) ListPages(ctx context.Context, folderFilter string, asTree bool) ([]*FolderInfo, error) {
	// Load state
	if err := c.loadState(ctx); err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	// Load all page registries
	registries, err := c.listPageRegistries(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to list registries", "error", err)
		registries = []*PageRegistry{}
	}

	// Group registries by folder
	folderPages := make(map[string][]*PageRegistry)
	for _, reg := range registries {
		folderPages[reg.Folder] = append(folderPages[reg.Folder], reg)
	}

	var folders []*FolderInfo

	for _, folderName := range c.state.Folders {
		// Filter by folder if specified
		if folderFilter != "" && folderName != folderFilter {
			continue
		}

		regs := folderPages[folderName]

		// Build page info map
		pageInfoMap := make(map[string]*PageInfo)
		regMap := make(map[string]*PageRegistry)
		orphanedCount := 0
		rootCount := 0

		for _, reg := range regs {
			regMap[reg.ID] = reg

			isOrphaned := false
			if reg.ParentID != "" {
				if _, exists := regMap[reg.ParentID]; !exists {
					// Check if parent exists
					if _, err := c.loadPageRegistry(ctx, reg.ParentID); err != nil {
						isOrphaned = true
						orphanedCount++
					}
				}
			}

			if reg.IsRoot {
				rootCount++
			}

			pageInfoMap[reg.ID] = &PageInfo{
				ID:         reg.ID,
				Title:      reg.Title,
				Path:       reg.FilePath,
				LastSynced: reg.LastSynced,
				IsRoot:     reg.IsRoot,
				IsOrphaned: isOrphaned,
				ParentID:   reg.ParentID,
				Children:   []*PageInfo{},
			}
		}

		// Build tree structure if requested
		var rootPages []*PageInfo
		if asTree {
			// Link children to parents
			for _, reg := range regs {
				info := pageInfoMap[reg.ID]
				for _, childID := range reg.Children {
					if childInfo, exists := pageInfoMap[childID]; exists {
						info.Children = append(info.Children, childInfo)
					}
				}
			}

			// Collect root pages
			for _, info := range pageInfoMap {
				if info.IsRoot {
					rootPages = append(rootPages, info)
				}
			}
		} else {
			// Flat list - just collect all pages
			for _, info := range pageInfoMap {
				rootPages = append(rootPages, info)
			}
		}

		folders = append(folders, &FolderInfo{
			Name:          folderName,
			RootPages:     rootCount,
			TotalPages:    len(regs),
			OrphanedPages: orphanedCount,
			Pages:         rootPages,
		})
	}

	return folders, nil
}

// ScanPage re-scans a page to discover all child pages and queues them.
// This is useful for discovering new child pages under an existing root page.
func (c *Crawler) ScanPage(ctx context.Context, pageID string) error {
	c.logger.InfoContext(ctx, "scanning page for children", "page_id", pageID)

	// Load state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Load page registry to get folder
	reg, err := c.loadPageRegistry(ctx, pageID)
	if err != nil {
		return fmt.Errorf("page not found in registry (use 'add' to add a new root page): %w", err)
	}

	folder := reg.Folder

	// Fetch blocks to discover children
	blocks, err := c.client.GetAllBlockChildren(ctx, pageID, 0)
	if err != nil {
		return fmt.Errorf("fetch blocks: %w", err)
	}

	// Discover child pages
	children := c.findChildPages(blocks)

	if len(children) == 0 {
		c.logger.InfoContext(ctx, "no child pages found", "page_id", pageID)
		return nil
	}

	c.logger.InfoContext(ctx, "discovered child pages",
		"page_id", pageID,
		"children_count", len(children))

	// Filter out children that are already tracked
	var newChildren []string
	for _, childID := range children {
		if _, err := c.loadPageRegistry(ctx, childID); err != nil {
			// Child doesn't exist yet
			newChildren = append(newChildren, childID)
		}
	}

	if len(newChildren) == 0 {
		c.logger.InfoContext(ctx, "all child pages are already tracked", "page_id", pageID)
		return nil
	}

	c.logger.InfoContext(ctx, "queueing new child pages",
		"page_id", pageID,
		"new_children_count", len(newChildren))

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Queue new children
	entry := queue.Entry{
		Type:     queueTypeInit,
		Folder:   folder,
		PageIDs:  newChildren,
		ParentID: pageID,
	}

	if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
		return fmt.Errorf("create queue entry: %w", err)
	}

	c.logger.InfoContext(ctx, "scan complete",
		"page_id", pageID,
		"total_children", len(children),
		"new_children", len(newChildren),
		"already_tracked", len(children)-len(newChildren))

	return nil
}

// GetStatus returns status information.
func (c *Crawler) GetStatus(ctx context.Context, folderFilter string) (*StatusInfo, error) {
	// Load state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Load all page registries
	registries, err := c.listPageRegistries(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to list registries", "error", err)
		registries = []*PageRegistry{}
	}

	status := &StatusInfo{
		Folders: make(map[string]*FolderStatus),
	}

	// Group registries by folder
	folderPages := make(map[string][]*PageRegistry)
	for _, reg := range registries {
		folderPages[reg.Folder] = append(folderPages[reg.Folder], reg)
	}

	// Gather folder statistics
	for _, folderName := range c.state.Folders {
		if folderFilter != "" && folderName != folderFilter {
			continue
		}

		regs := folderPages[folderName]

		// Find most recent sync time and count roots
		var lastSynced *time.Time
		rootCount := 0
		for _, reg := range regs {
			if lastSynced == nil || reg.LastSynced.After(*lastSynced) {
				t := reg.LastSynced
				lastSynced = &t
			}
			if reg.IsRoot {
				rootCount++
			}
		}

		status.Folders[folderName] = &FolderStatus{
			Name:       folderName,
			PageCount:  len(regs),
			RootPages:  rootCount,
			LastSynced: lastSynced,
		}

		status.FolderCount++
		status.TotalPages += len(regs)
		status.TotalRootPages += rootCount
	}

	// Get queue information
	queueFiles, err := c.queueManager.ListEntries(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to list queue entries", "error", err)
	} else {
		for _, queueFile := range queueFiles {
			entry, err := c.queueManager.ReadEntry(ctx, queueFile)
			if err != nil {
				c.logger.WarnContext(ctx, "failed to read queue entry", "file", queueFile, "error", err)
				continue
			}

			// Filter by folder if specified
			if folderFilter != "" && entry.Folder != folderFilter {
				continue
			}

			status.QueueEntries = append(status.QueueEntries, &QueueInfo{
				Folder:    entry.Folder,
				Type:      entry.Type,
				PageCount: len(entry.PageIDs),
				QueueFile: queueFile,
			})

			// Add to folder queued pages count
			if folderStatus, exists := status.Folders[entry.Folder]; exists {
				folderStatus.QueuedPages += len(entry.PageIDs)
			}
		}
	}

	return status, nil
}
