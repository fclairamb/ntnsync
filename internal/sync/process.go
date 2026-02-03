package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/notion"
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
		"duration_ms", time.Since(startTime).Milliseconds(),
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

// blockParentResult holds the result of block parent resolution.
type blockParentResult struct {
	parentID string
	isRoot   bool
}

// resolveBlockParentWithLogging resolves a block parent to its containing page/workspace,
// with appropriate logging. Returns (parentID, isRoot).
func (c *Crawler) resolveBlockParentWithLogging(
	ctx context.Context, itemID, itemType, blockID string, parent notion.Parent,
) blockParentResult {
	if parent.Type != parentTypeBlockID {
		return blockParentResult{parentID: normalizePageID(parent.ID())}
	}

	c.logger.DebugContext(ctx, "parent is a block, resolving to containing page",
		itemType, itemID,
		"block_id", blockID)

	resolvedID, resolvedType, resolveErr := c.resolveBlockToPage(ctx, blockID)
	switch {
	case resolveErr != nil:
		c.logger.WarnContext(ctx, "failed to resolve block parent, treating as root",
			itemType, itemID,
			"block_id", blockID,
			"error", resolveErr)
		return blockParentResult{isRoot: true}
	case resolvedType == parentTypeWorkspace:
		c.logger.DebugContext(ctx, "block parent resolves to workspace, treating as root",
			itemType, itemID)
		return blockParentResult{isRoot: true}
	default:
		c.logger.InfoContext(ctx, "resolved block parent to page",
			itemType, itemID,
			"block_id", blockID,
			"resolved_parent_id", resolvedID)
		return blockParentResult{parentID: resolvedID}
	}
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

// writeAndRegisterParams holds parameters for writeAndRegister.
type writeAndRegisterParams struct {
	itemID           string
	itemType         string // "page" or "database" (for logging and registry)
	title            string
	lastEdited       time.Time
	parent           notion.Parent
	folder           string
	isInit           bool
	expectedParentID string
	existingReg      *PageRegistry
	enabled          bool

	// convert generates the markdown content given the resolved file path, isRoot, and parentID.
	convert          func(filePath string, isRoot bool, parentID string) []byte
	downloadDuration time.Duration

	// Children
	children []string
}

// writeAndRegister handles parent resolution, file path computation, conversion, writing,
// registry saving, and child queuing. Used by processPage for both pages and databases.
//
//nolint:funlen // Shared logic for page/database processing
func (c *Crawler) writeAndRegister(
	ctx context.Context, startTime time.Time, p *writeAndRegisterParams,
) (int, error) {
	filesWritten := 0
	logKey := p.itemType + "_id"

	// Determine parent (resolving block parent if needed)
	blockRes := c.resolveBlockParentWithLogging(ctx, p.itemID, logKey, p.parent.BlockID, p.parent)
	parentID := blockRes.parentID
	isRoot := blockRes.isRoot

	// Resolve and fetch parent if needed
	parentResult, err := c.resolveAndFetchParent(
		ctx, p.itemID, logKey, parentID, p.expectedParentID, p.folder, p.isInit, isRoot)
	if err != nil {
		return 0, err
	}
	parentID = parentResult.parentID
	isRoot = parentResult.isRoot
	filesWritten += parentResult.filesWritten

	// Compute file path using a synthetic page (computeFilePath checks registry first for stability)
	syntheticPage := &notion.Page{
		ID:     p.itemID,
		Parent: p.parent,
		Properties: notion.Properties{
			"title": {Type: "title", Title: []notion.RichText{{PlainText: p.title}}},
		},
	}
	filePath := c.computeFilePath(ctx, syntheticPage, p.folder, isRoot, parentID)

	now := time.Now()

	// Convert to markdown with resolved path, isRoot, and parentID
	content := p.convert(filePath, isRoot, parentID)

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write file
	writeStart := time.Now()
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return 0, fmt.Errorf("write %s: %w", p.itemType, err)
	}
	writeDuration := time.Since(writeStart)
	filesWritten++

	totalDuration := time.Since(startTime)
	c.logger.InfoContext(ctx, "downloaded "+p.itemType,
		logKey, p.itemID,
		"title", p.title,
		"path", filePath,
		"total_ms", totalDuration.Milliseconds(),
		"download_ms", p.downloadDuration.Milliseconds(),
		"write_ms", writeDuration.Milliseconds())

	// Preserve IsRoot and Enabled from existing registry (set by ReconcileRootMd)
	if p.existingReg != nil && p.existingReg.IsRoot {
		isRoot = true
		p.enabled = p.existingReg.Enabled
	}

	// Save page registry
	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             p.itemID,
		Type:           p.itemType,
		Folder:         p.folder,
		FilePath:       filePath,
		Title:          p.title,
		LastEdited:     p.lastEdited,
		LastSynced:     now,
		IsRoot:         isRoot,
		Enabled:        p.enabled,
		ParentID:       parentID,
		Children:       p.children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Queue children if they don't exist yet
	var newChildren []string
	for _, childID := range p.children {
		if _, err := c.loadPageRegistry(ctx, childID); err != nil {
			newChildren = append(newChildren, childID)
		}
	}

	if len(newChildren) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   p.folder,
			PageIDs:  newChildren,
			ParentID: p.itemID,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue child pages", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued child pages", "count", len(newChildren), "parent_id", p.itemID)
		}
	}

	return filesWritten, nil
}

// verifyNewItemRoot checks that a new item (not in registry) belongs to an enabled root.
// Returns the updated folder and whether processing should continue.
func (c *Crawler) verifyNewItemRoot(
	ctx context.Context, page *notion.Page, itemID, logKey, folder string,
) (string, bool) {
	_, detectedFolder, foundRoot, err := c.traceParentChain(ctx, page, folder)
	if err != nil {
		c.logger.WarnContext(ctx, "failed to trace parent chain",
			logKey, itemID,
			"error", err)
		return folder, false
	}
	if !foundRoot {
		c.logger.InfoContext(ctx, "skipping item not under any root in root.md",
			logKey, itemID)
		return folder, false
	}
	if folder != detectedFolder {
		c.logger.DebugContext(ctx, "using folder from parent chain",
			logKey, itemID,
			"requested_folder", folder,
			"detected_folder", detectedFolder)
		folder = detectedFolder
	}
	return folder, true
}

// processPage fetches and saves a single page or database.
// expectedParentID is an optional hint from the queue entry about the expected parent.
// Returns (filesWritten, error).
func (c *Crawler) processPage(
	ctx context.Context, pageID, folder string, isInit bool, expectedParentID string,
) (int, error) {
	startTime := time.Now()
	c.logger.DebugContext(ctx, "processing page",
		"page_id", pageID,
		"folder", folder,
		"is_init", isInit,
		"expected_parent_id", expectedParentID)

	// Check if this item's root is enabled
	enabled, rootID, err := c.isRootEnabled(ctx, pageID)
	if err == nil && !enabled && rootID != "" {
		c.logger.InfoContext(ctx, "skipping item with disabled root",
			"page_id", pageID,
			"root_id", rootID)
		return 0, nil
	}

	// Try to fetch as page first
	fetchStart := time.Now()
	page, fetchErr := c.client.GetPage(ctx, pageID)
	isDatabase := fetchErr != nil && strings.Contains(fetchErr.Error(), "is a database, not a page")
	if fetchErr != nil && !isDatabase {
		return 0, fmt.Errorf("fetch page: %w", fetchErr)
	}

	var params *writeAndRegisterParams

	if isDatabase {
		c.logger.InfoContext(ctx, "detected database, processing as database", "page_id", pageID)
		params, folder, err = c.buildDatabaseParams(ctx, pageID, folder, fetchStart)
	} else {
		c.logger.DebugContext(ctx, "fetched page metadata",
			"page_id", pageID, "duration_ms", time.Since(fetchStart).Milliseconds())
		c.enrichUsers(ctx, &page.CreatedBy, &page.LastEditedBy)
		params, folder, err = c.buildPageParams(ctx, page, pageID, folder, fetchStart)
	}
	if err != nil {
		return 0, err
	}

	// For new items (not in registry), verify they belong to an enabled root
	existingReg, _ := c.loadPageRegistry(ctx, pageID)
	if existingReg == nil {
		syntheticPage := &notion.Page{ID: pageID, Parent: params.parent}
		var ok bool
		if folder, ok = c.verifyNewItemRoot(ctx, syntheticPage, pageID, params.itemType+"_id", folder); !ok {
			return 0, nil
		}
	}

	params.folder = folder
	params.isInit = isInit
	params.expectedParentID = expectedParentID
	params.existingReg = existingReg
	params.enabled = enabled

	return c.writeAndRegister(ctx, startTime, params)
}

// buildPageParams fetches blocks and builds writeAndRegisterParams for a page.
func (c *Crawler) buildPageParams(
	ctx context.Context, page *notion.Page, pageID, folder string, fetchStart time.Time,
) (*writeAndRegisterParams, string, error) {
	fetchPageDuration := time.Since(fetchStart)

	fetchBlocksStart := time.Now()
	maxDepth := getBlockDepthLimit()
	blockResult, err := c.client.GetAllBlockChildrenWithLimit(ctx, pageID, maxDepth)
	if err != nil {
		return nil, folder, fmt.Errorf("fetch blocks: %w", err)
	}

	blocks := blockResult.Blocks
	fetchBlocksDuration := time.Since(fetchBlocksStart)
	logArgs := []any{
		"page_id", pageID,
		"block_count", len(blocks),
		"duration_ms", fetchBlocksDuration.Milliseconds(),
	}
	if blockResult.WasLimited {
		logArgs = append(logArgs, "simplified_depth", blockResult.MaxDepth)
	}
	c.logger.DebugContext(ctx, "fetched page blocks", logArgs...)

	simplifiedDepth := 0
	if blockResult.WasLimited {
		simplifiedDepth = blockResult.MaxDepth
	}

	downloadDuration := fetchPageDuration + fetchBlocksDuration
	children := c.findChildPages(blocks)

	return &writeAndRegisterParams{
		itemID:   pageID,
		itemType: "page",
		title:    page.Title(),
		convert: func(filePath string, isRoot bool, parentID string) []byte {
			return c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
				Folder:           folder,
				PageTitle:        page.Title(),
				FilePath:         filePath,
				LastSynced:       time.Now(),
				NotionType:       "page",
				IsRoot:           isRoot,
				ParentID:         parentID,
				FileProcessor:    c.makeFileProcessor(ctx, filePath, pageID),
				SimplifiedDepth:  simplifiedDepth,
				DownloadDuration: downloadDuration,
			})
		},
		lastEdited:       page.LastEditedTime,
		parent:           page.Parent,
		downloadDuration: downloadDuration,
		children:         children,
	}, folder, nil
}

// buildDatabaseParams fetches database metadata and pages, and builds writeAndRegisterParams.
func (c *Crawler) buildDatabaseParams(
	ctx context.Context, databaseID, folder string, fetchStart time.Time,
) (*writeAndRegisterParams, string, error) {
	database, err := c.client.GetDatabase(ctx, databaseID)
	fetchDBDuration := time.Since(fetchStart)
	if err != nil {
		return nil, folder, fmt.Errorf("fetch database: %w", err)
	}
	c.logger.DebugContext(ctx, "fetched database metadata",
		"database_id", databaseID,
		"duration_ms", fetchDBDuration.Milliseconds())

	c.enrichUsers(ctx, &database.CreatedBy, &database.LastEditedBy)

	queryDBStart := time.Now()
	dbPages, err := c.client.QueryDatabase(ctx, databaseID)
	queryDBDuration := time.Since(queryDBStart)
	if err != nil {
		return nil, folder, fmt.Errorf("query database: %w", err)
	}
	c.logger.DebugContext(ctx, "queried database pages",
		"database_id", databaseID,
		"page_count", len(dbPages),
		"duration_ms", queryDBDuration.Milliseconds())

	dbID := normalizePageID(databaseID)
	downloadDuration := fetchDBDuration + queryDBDuration

	var children []string
	for i := range dbPages {
		children = append(children, normalizePageID(dbPages[i].ID))
	}

	return &writeAndRegisterParams{
		itemID:   dbID,
		itemType: "database",
		title:    database.GetTitle(),
		convert: func(filePath string, isRoot bool, parentID string) []byte {
			return c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
				Folder:           folder,
				PageTitle:        database.GetTitle(),
				FilePath:         filePath,
				LastSynced:       time.Now(),
				NotionType:       "database",
				IsRoot:           isRoot,
				ParentID:         parentID,
				FileProcessor:    c.makeFileProcessor(ctx, filePath, dbID),
				DownloadDuration: downloadDuration,
			})
		},
		lastEdited:       database.LastEditedTime,
		parent:           database.Parent,
		downloadDuration: downloadDuration,
		children:         children,
	}, folder, nil
}
