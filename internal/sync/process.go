package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/version"
)

// getQueueDelay returns the delay between processing queue files.
func getQueueDelay() time.Duration {
	return GetConfig().QueueDelay
}

// getBlockDepthLimit returns the maximum depth for block discovery.
// Returns 0 for unlimited depth (default).
func getBlockDepthLimit() int {
	return GetConfig().BlockDepth
}

// QueueCallback is called after each queue file is processed (written or deleted).
type QueueCallback func() error

// ProcessQueue processes all queue entries, optionally filtered by folder.
// maxPages limits the number of pages to fetch (0 = unlimited).
// maxTime limits the duration of the sync (0 = unlimited).
func (c *Crawler) ProcessQueue(
	ctx context.Context, folderFilter string, maxPages int, maxFiles int, maxQueueFiles int, maxTime time.Duration,
) error {
	return c.ProcessQueueWithCallback(ctx, folderFilter, maxPages, maxFiles, maxQueueFiles, maxTime, nil)
}

// ProcessQueueWithCallback is like ProcessQueue but calls the callback after each queue file is processed.
//
//nolint:funlen,gocognit // Complex queue processing with multiple conditions and callbacks
func (c *Crawler) ProcessQueueWithCallback(
	ctx context.Context,
	folderFilter string,
	maxPages, maxFiles, maxQueueFiles int,
	maxTime time.Duration,
	callback QueueCallback,
) error {
	c.logger.InfoContext(ctx, "processing queue",
		"folder_filter", folderFilter,
		"max_pages", maxPages,
		"max_files", maxFiles,
		"max_queue_files", maxQueueFiles,
		"max_time", maxTime,
		"queue_delay", getQueueDelay())

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Load state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	totalProcessed := 0
	totalSkipped := 0
	totalFilesWritten := 0
	totalQueueFilesProcessed := 0
	startTime := time.Now()
	skippedFiles := make(map[string]bool) // Track files skipped due to folder filter or read errors

	// Check if we should stop based on limits
	shouldStop := func() bool {
		if maxPages > 0 && totalProcessed >= maxPages {
			return true
		}
		if maxFiles > 0 && totalFilesWritten >= maxFiles {
			return true
		}
		if maxTime > 0 && time.Since(startTime) >= maxTime {
			return true
		}
		return false
	}

	// Process queue files in alphabetical order, re-fetching after each file
	// to pick up any newly added files (e.g., from webhooks) with lower IDs
	for !shouldStop() {
		if maxQueueFiles > 0 && totalQueueFilesProcessed >= maxQueueFiles {
			break
		}

		// Re-fetch queue entries to get the current first file alphabetically
		queueFiles, err := c.queueManager.ListEntries(ctx)
		if err != nil {
			return fmt.Errorf("list queue entries: %w", err)
		}

		// Find the first file that hasn't been skipped
		var queueFile string
		for _, f := range queueFiles {
			if !skippedFiles[f] {
				queueFile = f
				break
			}
		}

		if queueFile == "" {
			c.logger.InfoContext(ctx, "queue is empty")
			break
		}

		entry, err := c.queueManager.ReadEntry(ctx, queueFile)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to read queue entry",
				"file", queueFile,
				"error", err)
			skippedFiles[queueFile] = true
			continue
		}

		// Filter by folder if specified
		if folderFilter != "" && entry.Folder != folderFilter {
			c.logger.DebugContext(ctx, "skipping queue entry for different folder",
				"file", queueFile,
				"folder", entry.Folder)
			skippedFiles[queueFile] = true
			continue
		}

		// Apply queue delay before processing (if configured)
		queueDelay := getQueueDelay()
		if queueDelay > 0 {
			c.logger.InfoContext(ctx, "waiting before processing queue entry",
				"delay", queueDelay,
				"file", queueFile)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(queueDelay):
			}
		}

		c.logger.InfoContext(ctx, "processing queue entry",
			"file", queueFile,
			"type", entry.Type,
			"folder", entry.Folder,
			"pages", len(entry.PageIDs))

		// Ensure folder is in state
		c.state.AddFolder(entry.Folder)

		// Process each page in the entry (supports both old and new formats)
		stats := &queueProcessingStats{
			totalProcessed:    totalProcessed,
			totalSkipped:      totalSkipped,
			totalFilesWritten: totalFilesWritten,
		}

		var remainingPageIDs []string
		var remainingPages []queue.Page

		if len(entry.Pages) > 0 {
			remainingPages = c.processNewFormatEntry(ctx, entry, stats, shouldStop)
		} else {
			remainingPageIDs = c.processLegacyFormatEntry(ctx, entry, stats, shouldStop)
		}

		totalProcessed = stats.totalProcessed
		totalSkipped = stats.totalSkipped
		totalFilesWritten = stats.totalFilesWritten

		// Update or delete queue entry based on remaining pages
		c.updateOrDeleteQueueEntry(ctx, queueFile, entry, remainingPages, remainingPageIDs)

		// Mark as processed if there are remaining pages (will retry next sync cycle)
		if len(remainingPages) > 0 || len(remainingPageIDs) > 0 {
			skippedFiles[queueFile] = true
		}

		totalQueueFilesProcessed++

		// Call callback after queue file is processed (for periodic commits)
		if callback != nil {
			if err := callback(); err != nil {
				return fmt.Errorf("queue callback: %w", err)
			}
		}
	}

	// Final state save
	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Log completion with limit status
	logAttrs := []any{
		"processed", totalProcessed,
		"skipped", totalSkipped,
		"files_written", totalFilesWritten,
		"queue_files", totalQueueFilesProcessed,
		"elapsed", time.Since(startTime),
	}

	limitReached := false
	switch {
	case maxPages > 0 && totalProcessed >= maxPages:
		logAttrs = append(logAttrs, "limit_reached", "max_pages")
		limitReached = true
	case maxFiles > 0 && totalFilesWritten >= maxFiles:
		logAttrs = append(logAttrs, "limit_reached", "max_files")
		limitReached = true
	case maxQueueFiles > 0 && totalQueueFilesProcessed >= maxQueueFiles:
		logAttrs = append(logAttrs, "limit_reached", "max_queue_files")
		limitReached = true
	case maxTime > 0 && time.Since(startTime) >= maxTime:
		logAttrs = append(logAttrs, "limit_reached", "max_time")
		limitReached = true
	}

	if limitReached {
		c.logger.InfoContext(ctx, "queue processing stopped (limit reached)", logAttrs...)
	} else {
		c.logger.InfoContext(ctx, "queue processing complete", logAttrs...)
	}

	return nil
}

// updateOrDeleteQueueEntry updates the queue entry with remaining pages or deletes it if complete.
func (c *Crawler) updateOrDeleteQueueEntry(
	ctx context.Context,
	queueFile string,
	entry *queue.Entry,
	remainingPages []queue.Page,
	remainingPageIDs []string,
) {
	hasRemaining := len(remainingPages) > 0 || len(remainingPageIDs) > 0
	if !hasRemaining {
		if err := c.queueManager.DeleteEntry(ctx, queueFile); err != nil {
			c.logger.WarnContext(ctx, "failed to delete queue entry", "error", err)
		}
		return
	}

	// Update entry with remaining pages
	if len(remainingPages) > 0 {
		entry.Pages = remainingPages
		entry.PageIDs = nil // Clear legacy field
	} else {
		entry.PageIDs = remainingPageIDs
	}
	if err := c.queueManager.UpdateEntry(ctx, queueFile, entry); err != nil {
		c.logger.WarnContext(ctx, "failed to update queue entry", "error", err)
	}
}

// queueProcessingStats tracks statistics during queue processing.
type queueProcessingStats struct {
	totalProcessed    int
	totalSkipped      int
	totalFilesWritten int
}

// processNewFormatEntry processes pages in new format and returns remaining pages.
func (c *Crawler) processNewFormatEntry(
	ctx context.Context,
	entry *queue.Entry,
	stats *queueProcessingStats,
	shouldStop func() bool,
) []queue.Page {
	var remaining []queue.Page

	for i := range entry.Pages {
		queuePage := &entry.Pages[i]
		pageID := queuePage.ID

		if shouldStop() {
			remaining = append(remaining, *queuePage)
			continue
		}

		if c.shouldSkipNewFormatPage(ctx, pageID, queuePage.LastEdited) {
			stats.totalSkipped++
			continue
		}

		filesCount, err := c.processPage(ctx, pageID, entry.Folder, entry.Type == queueTypeInit, entry.ParentID)
		if err != nil {
			c.logger.ErrorContext(ctx, "failed to process page", "page_id", pageID, "error", err)
			remaining = append(remaining, *queuePage)
			continue
		}

		stats.totalProcessed++
		stats.totalFilesWritten += filesCount

		if stats.totalProcessed%10 == 0 {
			if err := c.saveState(ctx); err != nil {
				c.logger.WarnContext(ctx, "failed to save state", "error", err)
			}
		}
	}

	return remaining
}

// processLegacyFormatEntry processes pages in legacy format and returns remaining page IDs.
func (c *Crawler) processLegacyFormatEntry(
	ctx context.Context,
	entry *queue.Entry,
	stats *queueProcessingStats,
	shouldStop func() bool,
) []string {
	var remaining []string

	for _, pageID := range entry.PageIDs {
		if shouldStop() {
			remaining = append(remaining, pageID)
			continue
		}

		switch c.shouldSkipLegacyPage(ctx, pageID, entry.Type == queueTypeInit) {
		case legacyPageSkip:
			stats.totalSkipped++
			continue
		case legacyPageSkipAndRequeue:
			remaining = append(remaining, pageID)
			continue
		case legacyPageProcess:
			// Continue to processing below
		}

		filesCount, err := c.processPage(ctx, pageID, entry.Folder, entry.Type == queueTypeInit, entry.ParentID)
		if err != nil {
			c.logger.ErrorContext(ctx, "failed to process page", "page_id", pageID, "error", err)
			remaining = append(remaining, pageID)
			continue
		}

		stats.totalProcessed++
		stats.totalFilesWritten += filesCount

		if stats.totalProcessed%10 == 0 {
			if err := c.saveState(ctx); err != nil {
				c.logger.WarnContext(ctx, "failed to save state", "error", err)
			}
		}
	}

	return remaining
}

// shouldSkipNewFormatPage checks if a new format queue page should be skipped.
// Returns true if the page exists and hasn't been edited since last sync.
func (c *Crawler) shouldSkipNewFormatPage(ctx context.Context, pageID string, queueLastEdited time.Time) bool {
	reg, err := c.loadPageRegistry(ctx, pageID)
	if err != nil {
		return false // Page not in registry, should process
	}

	// Page exists - check if it needs updating by comparing with queued last_edited
	if !queueLastEdited.After(reg.LastEdited) {
		c.logger.DebugContext(ctx, "skipping unchanged page",
			"page_id", pageID,
			"title", reg.Title,
			"queue_last_edited", queueLastEdited,
			"registry_last_edited", reg.LastEdited)
		return true
	}
	return false
}

// legacyPageSkipResult indicates what to do with a legacy page.
type legacyPageSkipResult int

const (
	legacyPageProcess        legacyPageSkipResult = iota // Process the page
	legacyPageSkip                                       // Skip and count as skipped
	legacyPageSkipAndRequeue                             // Skip but keep in queue for retry
)

// shouldSkipLegacyPage checks if a legacy format page should be skipped for init queue type.
func (c *Crawler) shouldSkipLegacyPage(ctx context.Context, pageID string, isInit bool) legacyPageSkipResult {
	if !isInit {
		return legacyPageProcess
	}

	reg, err := c.loadPageRegistry(ctx, pageID)
	if err != nil {
		return legacyPageProcess // Page not in registry, should process
	}

	// Page exists in registry - in init mode, skip it to avoid unnecessary API calls.
	// Init mode is for discovering new pages, not updating existing ones.
	// Use "update" queue type for forcing re-sync of existing pages.
	c.logger.DebugContext(ctx, "skipping existing page in init mode (using cache)",
		"page_id", pageID,
		"title", reg.Title)
	return legacyPageSkip
}

// parentResolutionResult holds the result of parent resolution.
type parentResolutionResult struct {
	parentID     string
	isRoot       bool
	filesWritten int
}

// resolveAndFetchParent resolves and fetches the parent page if needed.
// It handles the logic of checking expected parent, looking up registry, fetching recursively,
// and resolving block parents to their containing pages.
//
//nolint:funlen // Parent resolution with multiple edge cases
func (c *Crawler) resolveAndFetchParent(
	ctx context.Context,
	itemID, itemType string, // for logging (e.g., "page_id" or "database_id")
	parentID, expectedParentID, folder string,
	isInit, isRoot bool,
) (*parentResolutionResult, error) {
	result := &parentResolutionResult{
		parentID: parentID,
		isRoot:   isRoot,
	}

	if parentID == "" {
		if !isRoot {
			result.isRoot = true
		}
		return result, nil
	}

	// If expectedParentID matches, skip the check - we know parent exists from queue
	if expectedParentID != "" && normalizePageID(expectedParentID) == parentID {
		c.logger.DebugContext(ctx, "using expected parent from queue",
			itemType, itemID,
			"parent_id", parentID)
		return result, nil
	}

	// Check if parent is in this folder via registry
	if _, err := c.loadPageRegistry(ctx, parentID); err == nil {
		return result, nil
	}

	// Parent not found in registry
	// In init mode, queue the parent for later processing instead of fetching it now
	// This improves performance by deferring parent content fetching
	if isInit {
		c.logger.InfoContext(ctx, "parent not in registry, queuing for later (init mode)",
			itemType, itemID,
			"parent_id", parentID)

		// Queue parent page for processing
		if _, queueErr := c.queueManager.CreateEntry(ctx, queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  []string{parentID},
			ParentID: "", // Parent will determine its own parent
		}); queueErr != nil {
			c.logger.WarnContext(ctx, "failed to queue parent page",
				"parent_id", parentID,
				"error", queueErr)
		}

		// Treat child as root for now - will be reorganized when parent is processed
		result.isRoot = true
		result.parentID = ""
		return result, nil
	}

	// In update mode, fetch parent immediately for correct path resolution
	c.logger.InfoContext(ctx, "fetching parent page first",
		itemType, itemID,
		"parent_id", parentID)

	parentFiles, err := c.processPage(ctx, parentID, folder, isInit, "")
	if err == nil {
		result.filesWritten = parentFiles
		return result, nil
	}

	// If parent is a block (not a page), resolve to containing page
	if !strings.Contains(err.Error(), "is a block, not a page") {
		c.logger.ErrorContext(ctx, "failed to fetch parent, failing",
			itemType, itemID,
			"parent_id", parentID,
			"error", err)
		return nil, fmt.Errorf("failed to fetch parent page: %w", err)
	}

	c.logger.DebugContext(ctx, "parent ID is a block, resolving to containing page",
		itemType, itemID,
		"block_id", parentID)

	resolvedID, resolvedType, resolveErr := c.resolveBlockToPage(ctx, parentID)
	switch {
	case resolveErr != nil:
		c.logger.WarnContext(ctx, "failed to resolve block parent, treating as root",
			itemType, itemID,
			"block_id", parentID,
			"error", resolveErr)
		result.isRoot = true
		result.parentID = ""
	case resolvedType == parentTypeWorkspace:
		c.logger.DebugContext(ctx, "block resolves to workspace, treating as root",
			itemType, itemID)
		result.isRoot = true
		result.parentID = ""
	default:
		// Update parentID to the resolved page
		result.parentID = resolvedID
		c.logger.InfoContext(ctx, "resolved block to page, fetching parent",
			itemType, itemID,
			"resolved_parent_id", resolvedID)
		// Now try to fetch/process the resolved parent
		if _, loadErr := c.loadPageRegistry(ctx, resolvedID); loadErr == nil {
			// Resolved parent is in registry, we're done
			return result, nil
		}

		// Resolved parent not in registry
		if isInit {
			// In init mode, queue the resolved parent for later
			c.logger.InfoContext(ctx, "resolved parent not in registry, queuing for later (init mode)",
				itemType, itemID,
				"resolved_parent_id", resolvedID)

			if _, queueErr := c.queueManager.CreateEntry(ctx, queue.Entry{
				Type:     queueTypeInit,
				Folder:   folder,
				PageIDs:  []string{resolvedID},
				ParentID: "",
			}); queueErr != nil {
				c.logger.WarnContext(ctx, "failed to queue resolved parent",
					"parent_id", resolvedID,
					"error", queueErr)
			}

			// Treat child as root for now
			result.isRoot = true
			result.parentID = ""
			return result, nil
		}

		// In update mode, fetch immediately
		resolvedParentFiles, fetchErr := c.processPage(ctx, resolvedID, folder, isInit, "")
		if fetchErr != nil {
			c.logger.ErrorContext(ctx, "failed to fetch resolved parent, treating as root",
				itemType, itemID,
				"parent_id", resolvedID,
				"error", fetchErr)
			result.isRoot = true
			result.parentID = ""
		} else {
			result.filesWritten = resolvedParentFiles
		}
	}

	return result, nil
}

// processPage fetches and saves a single page or database.
// expectedParentID is an optional hint from the queue entry about the expected parent.
// processPage processes a single page and returns the number of markdown files written.
// Returns (filesWritten, error).
//
//nolint:funlen // Complex page processing with child queuing
func (c *Crawler) processPage(
	ctx context.Context, pageID, folder string, isInit bool, expectedParentID string,
) (int, error) {
	startTime := time.Now()
	c.logger.DebugContext(ctx, "processing page",
		"page_id", pageID,
		"folder", folder,
		"is_init", isInit,
		"expected_parent_id", expectedParentID)

	// Check if this page's root is enabled
	enabled, rootID, err := c.isRootEnabled(ctx, pageID)
	if err == nil && !enabled && rootID != "" {
		c.logger.InfoContext(ctx, "skipping page with disabled root",
			"page_id", pageID,
			"root_id", rootID)
		return 0, nil
	}

	filesWritten := 0

	// Try to fetch as page first
	fetchPageStart := time.Now()
	page, err := c.client.GetPage(ctx, pageID)
	fetchPageDuration := time.Since(fetchPageStart)
	if err != nil {
		// Check if this is actually a database
		if strings.Contains(err.Error(), "is a database, not a page") {
			c.logger.InfoContext(ctx, "detected database, processing as database", "page_id", pageID)
			return c.processDatabase(ctx, pageID, folder, isInit, expectedParentID)
		}
		return 0, fmt.Errorf("fetch page: %w", err)
	}
	c.logger.DebugContext(ctx, "fetched page metadata", "page_id", pageID, "duration_ms", fetchPageDuration.Milliseconds())

	// Fetch blocks with optional depth limit
	fetchBlocksStart := time.Now()
	maxDepth := getBlockDepthLimit()
	blockResult, err := c.client.GetAllBlockChildrenWithLimit(ctx, pageID, maxDepth)
	fetchBlocksDuration := time.Since(fetchBlocksStart)
	if err != nil {
		return 0, fmt.Errorf("fetch blocks: %w", err)
	}
	blocks := blockResult.Blocks
	logArgs := []any{
		"page_id", pageID,
		"block_count", len(blocks),
		"duration_ms", fetchBlocksDuration.Milliseconds(),
	}
	if blockResult.WasLimited {
		logArgs = append(logArgs, "simplified_depth", blockResult.MaxDepth)
	}
	c.logger.DebugContext(ctx, "fetched page blocks", logArgs...)

	// Determine if this is a root page (check parent)
	isRoot := false
	parentID := ""

	// Pages can have a page, database, or block as parent
	// If parent is a block (e.g., toggle, synced block, column), resolve to containing page
	if page.Parent.Type == parentTypeBlockID {
		c.logger.DebugContext(ctx, "page parent is a block, resolving to containing page",
			"page_id", pageID,
			"block_id", page.Parent.BlockID)
		resolvedID, resolvedType, resolveErr := c.resolveBlockToPage(ctx, page.Parent.BlockID)
		switch {
		case resolveErr != nil:
			c.logger.WarnContext(ctx, "failed to resolve block parent, treating as root",
				"page_id", pageID,
				"block_id", page.Parent.BlockID,
				"error", resolveErr)
			isRoot = true
		case resolvedType == parentTypeWorkspace:
			c.logger.DebugContext(ctx, "block parent resolves to workspace, treating as root",
				"page_id", pageID)
			isRoot = true
		default:
			parentID = resolvedID
			c.logger.InfoContext(ctx, "resolved block parent to page",
				"page_id", pageID,
				"block_id", page.Parent.BlockID,
				"resolved_parent_id", parentID)
		}
	} else {
		parentID = normalizePageID(page.Parent.ID())
	}

	// Resolve and fetch parent if needed
	parentResult, err := c.resolveAndFetchParent(
		ctx, pageID, "page_id", parentID, expectedParentID, folder, isInit, isRoot)
	if err != nil {
		return 0, err
	}
	parentID = parentResult.parentID
	isRoot = parentResult.isRoot
	filesWritten += parentResult.filesWritten

	// Compute file path (using resolved parent ID)
	filePath := c.computeFilePath(ctx, page, folder, isRoot, parentID)

	now := time.Now()

	// Only set SimplifiedDepth if limiting actually occurred
	simplifiedDepth := 0
	if blockResult.WasLimited {
		simplifiedDepth = blockResult.MaxDepth
	}

	// Calculate download duration (API fetch time)
	downloadDuration := fetchPageDuration + fetchBlocksDuration

	// Convert to markdown
	convertStart := time.Now()
	content := c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
		Folder:           folder,
		PageTitle:        page.Title(),
		FilePath:         filePath,
		LastSynced:       now,
		NotionType:       "page",
		IsRoot:           isRoot,
		ParentID:         parentID,
		FileProcessor:    c.makeFileProcessor(ctx, filePath, pageID),
		SimplifiedDepth:  simplifiedDepth,
		DownloadDuration: downloadDuration,
	})
	convertDuration := time.Since(convertStart)

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write file
	writeStart := time.Now()
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return 0, fmt.Errorf("write page: %w", err)
	}
	writeDuration := time.Since(writeStart)
	filesWritten++ // Count this file write

	totalDuration := time.Since(startTime)
	downloadLogArgs := []any{
		"page_id", pageID,
		"title", page.Title(),
		"path", filePath,
		"total_ms", totalDuration.Milliseconds(),
		"fetch_page_ms", fetchPageDuration.Milliseconds(),
		"fetch_blocks_ms", fetchBlocksDuration.Milliseconds(),
		"convert_ms", convertDuration.Milliseconds(),
		"write_ms", writeDuration.Milliseconds(),
	}
	if simplifiedDepth > 0 {
		downloadLogArgs = append(downloadLogArgs, "simplified_depth", simplifiedDepth)
	}
	c.logger.InfoContext(ctx, "downloaded page", downloadLogArgs...)

	// Discover child pages
	children := c.findChildPages(blocks)

	// Save page registry
	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             pageID,
		Type:           "page",
		Folder:         folder,
		FilePath:       filePath,
		Title:          page.Title(),
		LastEdited:     page.LastEditedTime,
		LastSynced:     now,
		IsRoot:         isRoot,
		ParentID:       parentID,
		Children:       children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Queue child pages if they don't exist yet
	var newChildren []string
	for _, childID := range children {
		if _, err := c.loadPageRegistry(ctx, childID); err != nil {
			// Child doesn't exist yet
			newChildren = append(newChildren, childID)
		}
	}

	if len(newChildren) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  newChildren,
			ParentID: pageID, // Set parent ID for proper folder structure
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue child pages", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued child pages", "count", len(newChildren), "parent_id", pageID)
		}
	}

	return filesWritten, nil
}

// processDatabase fetches and saves a database with its page links.
// expectedParentID is an optional hint from the queue entry about the expected parent.
// Returns (filesWritten, error).
//
//nolint:funlen // Complex database processing with child queuing
func (c *Crawler) processDatabase(
	ctx context.Context, databaseID, folder string, isInit bool, expectedParentID string,
) (int, error) {
	startTime := time.Now()
	c.logger.DebugContext(ctx, "processing database",
		"database_id", databaseID,
		"folder", folder,
		"is_init", isInit,
		"expected_parent_id", expectedParentID)

	// Check if this database's root is enabled
	enabled, rootID, err := c.isRootEnabled(ctx, databaseID)
	if err == nil && !enabled && rootID != "" {
		c.logger.InfoContext(ctx, "skipping database with disabled root",
			"database_id", databaseID,
			"root_id", rootID)
		return 0, nil
	}

	filesWritten := 0

	// Fetch database metadata
	fetchDBStart := time.Now()
	database, err := c.client.GetDatabase(ctx, databaseID)
	fetchDBDuration := time.Since(fetchDBStart)
	if err != nil {
		return 0, fmt.Errorf("fetch database: %w", err)
	}
	c.logger.DebugContext(ctx, "fetched database metadata",
		"database_id", databaseID,
		"duration_ms", fetchDBDuration.Milliseconds())

	// Query all pages in the database
	queryDBStart := time.Now()
	dbPages, err := c.client.QueryDatabase(ctx, databaseID)
	queryDBDuration := time.Since(queryDBStart)
	if err != nil {
		return 0, fmt.Errorf("query database: %w", err)
	}
	c.logger.DebugContext(ctx, "queried database pages",
		"database_id", databaseID,
		"page_count", len(dbPages),
		"duration_ms", queryDBDuration.Milliseconds())

	// Determine if this is a root database (check parent)
	isRoot := false
	parentID := ""

	// Databases can have a page, database, or block as parent
	// If parent is a block (e.g., toggle, synced block, column), resolve to containing page
	if database.Parent.Type == parentTypeBlockID {
		c.logger.DebugContext(ctx, "database parent is a block, resolving to containing page",
			"database_id", databaseID,
			"block_id", database.Parent.BlockID)
		resolvedID, resolvedType, resolveErr := c.resolveBlockToPage(ctx, database.Parent.BlockID)
		switch {
		case resolveErr != nil:
			c.logger.WarnContext(ctx, "failed to resolve block parent, treating as root",
				"database_id", databaseID,
				"block_id", database.Parent.BlockID,
				"error", resolveErr)
			isRoot = true
		case resolvedType == parentTypeWorkspace:
			c.logger.DebugContext(ctx, "block parent resolves to workspace, treating as root",
				"database_id", databaseID)
			isRoot = true
		default:
			parentID = resolvedID
			c.logger.InfoContext(ctx, "resolved block parent to page",
				"database_id", databaseID,
				"block_id", database.Parent.BlockID,
				"resolved_parent_id", parentID)
		}
	} else {
		parentID = normalizePageID(database.Parent.ID())
	}

	// Resolve and fetch parent if needed
	parentResult, err := c.resolveAndFetchParent(
		ctx, databaseID, "database_id", parentID, expectedParentID, folder, isInit, isRoot)
	if err != nil {
		return 0, err
	}
	parentID = parentResult.parentID
	isRoot = parentResult.isRoot
	filesWritten += parentResult.filesWritten

	// Compute file path
	// For databases, we compute path similar to pages
	dbID := normalizePageID(databaseID)

	// Check page registry for existing path (stability)
	var filePath string
	// Try to use existing registry path for stability
	if reg, err := c.loadPageRegistry(ctx, dbID); err == nil && reg.FilePath != "" {
		c.logger.DebugContext(ctx, "using registry path for stability",
			"database_id", dbID,
			"path", reg.FilePath)
		filePath = reg.FilePath
	} else {
		filePath = c.computeDatabasePath(ctx, database, dbID, folder, isRoot, parentID)
	}

	now := time.Now()

	// Calculate download duration (API fetch time)
	downloadDuration := fetchDBDuration + queryDBDuration

	// Convert to markdown
	convertStart := time.Now()
	content := c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
		Folder:           folder,
		PageTitle:        database.GetTitle(),
		FilePath:         filePath,
		LastSynced:       now,
		NotionType:       "database",
		IsRoot:           isRoot,
		ParentID:         parentID,
		FileProcessor:    c.makeFileProcessor(ctx, filePath, dbID),
		DownloadDuration: downloadDuration,
	})
	convertDuration := time.Since(convertStart)

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write file
	writeStart := time.Now()
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return 0, fmt.Errorf("write database: %w", err)
	}
	writeDuration := time.Since(writeStart)
	filesWritten++ // Count this file write

	totalDuration := time.Since(startTime)
	c.logger.InfoContext(ctx, "downloaded database",
		"database_id", databaseID,
		"title", database.GetTitle(),
		"path", filePath,
		"pages_count", len(dbPages),
		"total_ms", totalDuration.Milliseconds(),
		"fetch_db_ms", fetchDBDuration.Milliseconds(),
		"query_db_ms", queryDBDuration.Milliseconds(),
		"convert_ms", convertDuration.Milliseconds(),
		"write_ms", writeDuration.Milliseconds())

	// Collect child page IDs from database
	var children []string
	for i := range dbPages {
		childID := normalizePageID(dbPages[i].ID)
		children = append(children, childID)
	}

	// Save page registry (treat database as a page in registry)
	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             dbID,
		Type:           "database",
		Folder:         folder,
		FilePath:       filePath,
		Title:          database.GetTitle(),
		LastEdited:     database.LastEditedTime,
		LastSynced:     now,
		IsRoot:         isRoot,
		ParentID:       parentID,
		Children:       children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Queue database pages if they don't exist yet
	var newChildren []string
	for _, childID := range children {
		if _, err := c.loadPageRegistry(ctx, childID); err != nil {
			// Child doesn't exist yet
			newChildren = append(newChildren, childID)
		}
	}

	if len(newChildren) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  newChildren,
			ParentID: dbID, // Set parent ID for proper folder structure
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue database pages", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued database pages", "count", len(newChildren), "parent_id", dbID)
		}
	}

	return filesWritten, nil
}
