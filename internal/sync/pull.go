package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
)

// PullOptions configures the pull operation.
type PullOptions struct {
	Folder   string        // Filter to specific folder (empty = all folders)
	Since    time.Duration // Override for cutoff time (0 = use LastPullTime)
	MaxPages int           // Maximum number of pages to queue (0 = unlimited)
	All      bool          // Include pages not yet tracked
	DryRun   bool          // Preview without modifying
	Verbose  bool          // Show detailed output
}

// PullResult contains the result of a pull operation.
type PullResult struct {
	PagesFound   int
	PagesQueued  int
	PagesSkipped int
	NewPages     int
	UpdatedPages int
	CutoffTime   time.Time
}

// Pull fetches all pages changed since the last pull and queues them for sync.
//
//nolint:funlen,gocognit // Complex pull logic with pagination and filtering
func (c *Crawler) Pull(ctx context.Context, opts PullOptions) (*PullResult, error) {
	c.logger.InfoContext(ctx, "starting pull",
		"folder", opts.Folder,
		"since", opts.Since,
		"all", opts.All,
		"dry_run", opts.DryRun)

	// Load state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Determine cutoff time
	var cutoffTime time.Time
	switch {
	case opts.Since > 0:
		cutoffTime = time.Now().Add(-opts.Since)
		c.logger.InfoContext(ctx, "using --since override", "cutoff_time", cutoffTime)
	case c.state.LastPullTime != nil:
		cutoffTime = *c.state.LastPullTime
		c.logger.InfoContext(ctx, "using last pull time", "cutoff_time", cutoffTime)
	default:
		return nil, apperrors.ErrNoPreviousPullTime
	}

	// Load all page registries to check what's tracked
	registries, err := c.listPageRegistries(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to list registries", "error", err)
		registries = []*PageRegistry{}
	}

	// Build map of tracked pages
	trackedPages := make(map[string]*PageRegistry)
	for _, reg := range registries {
		trackedPages[reg.ID] = reg
	}

	c.logger.InfoContext(ctx, "found tracked pages", "count", len(trackedPages))

	// Search all accessible pages with early stopping.
	// The Notion Search API does not support timestamp filtering.
	// We fetch pages (sorted newest first) and stop when reaching oldest_pull_result.
	c.logger.InfoContext(ctx, "searching for all accessible pages (sorted by last_edited_time)")

	// Early stop function - stops when we reach pages older than oldest_pull_result
	shouldStop := func(pages []notion.Page) bool {
		if c.state.OldestPullResult == nil || len(pages) == 0 {
			return false
		}
		// Check the last page in current batch
		lastPage := pages[len(pages)-1]
		if !lastPage.LastEditedTime.After(*c.state.OldestPullResult) {
			c.logger.InfoContext(ctx, "reached oldest pull result during fetch, stopping early",
				"last_page_time", lastPage.LastEditedTime,
				"oldest_pull_result", *c.state.OldestPullResult,
				"pages_fetched", len(pages))
			return true
		}
		return false
	}

	allPages, err := c.client.SearchAllPagesWithStop(ctx, shouldStop)
	if err != nil {
		return nil, fmt.Errorf("search pages: %w", err)
	}

	c.logger.InfoContext(ctx, "search complete", "pages_found", len(allPages))

	result := &PullResult{
		PagesFound: len(allPages),
		CutoffTime: cutoffTime,
	}

	// Group pages by folder and filter by changes
	pagesToQueue := make(map[string][]queue.Page) // folder -> []queue.Page
	var oldestPageSeen *time.Time
	pagesQueued := 0

	for i := range allPages {
		page := &allPages[i]
		pageID := normalizePageID(page.ID)

		// Check if we've reached the oldest pull result from previous pull
		if c.state.OldestPullResult != nil && !page.LastEditedTime.After(*c.state.OldestPullResult) {
			c.logger.DebugContext(ctx, "reached oldest pull result, stopping",
				"page_last_edited", page.LastEditedTime,
				"oldest_pull_result", *c.state.OldestPullResult)
			break
		}

		// Check if page was edited after cutoff
		if !page.LastEditedTime.After(cutoffTime) {
			result.PagesSkipped++
			continue
		}

		// Check MaxPages limit
		if opts.MaxPages > 0 && pagesQueued >= opts.MaxPages {
			c.logger.InfoContext(ctx, "reached max pages limit, stopping",
				"max_pages", opts.MaxPages)
			break
		}

		// Check if page is tracked
		reg, isTracked := trackedPages[pageID]

		if !isTracked && !opts.All {
			// Skip untracked pages unless --all flag is set
			c.logger.DebugContext(ctx, "skipping untracked page",
				"page_id", pageID,
				"title", page.Title())
			result.PagesSkipped++
			continue
		}

		// Determine folder
		var folder string
		if isTracked {
			folder = reg.Folder
			result.UpdatedPages++
		} else {
			// New page - need to determine folder by tracing parent chain
			parentChain, detectedFolder, err := c.traceParentChain(ctx, page, "")
			if err != nil {
				c.logger.WarnContext(ctx, "failed to trace parent chain, skipping",
					"page_id", pageID,
					"title", page.Title(),
					"error", err)
				result.PagesSkipped++
				continue
			}
			folder = detectedFolder
			result.NewPages++

			c.logger.InfoContext(ctx, "new page discovered",
				"page_id", pageID,
				"title", page.Title(),
				"folder", folder,
				"missing_parents", len(parentChain))
		}

		// Filter by folder if specified
		if opts.Folder != "" && folder != opts.Folder {
			c.logger.DebugContext(ctx, "skipping page in different folder",
				"page_id", pageID,
				"folder", folder)
			result.PagesSkipped++
			continue
		}

		// Add to queue list with last edited time
		queuePage := queue.Page{
			ID:         pageID,
			LastEdited: page.LastEditedTime,
		}
		pagesToQueue[folder] = append(pagesToQueue[folder], queuePage)
		pagesQueued++

		// Track oldest page seen
		if oldestPageSeen == nil || page.LastEditedTime.Before(*oldestPageSeen) {
			oldestPageSeen = &page.LastEditedTime
		}

		if opts.Verbose {
			c.logger.InfoContext(ctx, "page will be queued",
				"page_id", pageID,
				"title", page.Title(),
				"folder", folder,
				"last_edited", page.LastEditedTime)
		}
	}

	// Queue pages if not dry-run
	if opts.DryRun {
		result.PagesQueued = c.countPagesToQueue(pagesToQueue)
		c.logger.InfoContext(ctx, "dry run - no changes made")
	} else {
		if err := c.queuePagesForPull(ctx, pagesToQueue, oldestPageSeen, cutoffTime, result); err != nil {
			return nil, err
		}
	}

	c.logger.InfoContext(ctx, "pull complete",
		"pages_found", result.PagesFound,
		"pages_queued", result.PagesQueued,
		"pages_skipped", result.PagesSkipped,
		"new_pages", result.NewPages,
		"updated_pages", result.UpdatedPages)

	return result, nil
}

// countPagesToQueue counts the total number of pages to be queued.
func (c *Crawler) countPagesToQueue(pagesToQueue map[string][]queue.Page) int {
	total := 0
	for _, pages := range pagesToQueue {
		total += len(pages)
	}
	return total
}

// queuePagesForPull queues pages from a pull operation and updates state.
func (c *Crawler) queuePagesForPull(
	ctx context.Context, pagesToQueue map[string][]queue.Page,
	oldestPageSeen *time.Time, cutoffTime time.Time, result *PullResult,
) error {
	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	for folder, pages := range pagesToQueue {
		// Ensure folder is in state
		c.state.AddFolder(folder)

		// Create queue entry with type "update"
		entry := queue.Entry{
			Type:   "update",
			Folder: folder,
			Pages:  pages,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			return fmt.Errorf("create queue entry for folder %s: %w", folder, err)
		}

		c.logger.InfoContext(ctx, "queued pages",
			"folder", folder,
			"count", len(pages))
		result.PagesQueued += len(pages)
	}

	// Update LastPullTime and OldestPullResult
	now := time.Now()
	c.state.LastPullTime = &now
	if oldestPageSeen != nil {
		// Pages were queued - use the oldest queued page's timestamp
		c.state.OldestPullResult = oldestPageSeen
	} else {
		// No pages were queued - use cutoff time so next pull can stop early
		c.state.OldestPullResult = &cutoffTime
	}

	// Save state
	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	c.logger.InfoContext(ctx, "updated pull state",
		"last_pull_time", now,
		"oldest_pull_result", c.state.OldestPullResult)

	return nil
}

// loadState loads the state from disk.
func (c *Crawler) loadState(ctx context.Context) error {
	path := filepath.Join(stateDir, stateFile)
	data, err := c.store.Read(ctx, path)
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("unmarshal state: %w", err)
	}

	c.state = &state
	c.logger.DebugContext(ctx, "loaded state", "folders", len(state.Folders))
	return nil
}

// saveState saves the state to disk.
func (c *Crawler) saveState(ctx context.Context) error {
	data, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := filepath.Join(stateDir, stateFile)
	if err := c.tx.Write(path, data); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	c.logger.DebugContext(ctx, "saved state")
	return nil
}
