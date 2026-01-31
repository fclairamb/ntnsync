package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/version"
)

// AddDatabase adds all pages from a database to a folder.
//
//nolint:funlen // Complex database initialization logic
func (c *Crawler) AddDatabase(ctx context.Context, databaseID, folder string, forceUpdate bool) error {
	c.logger.InfoContext(ctx, "adding database",
		"database_id", databaseID,
		"folder", folder,
		"force_update", forceUpdate)

	// Validate folder name
	if err := validateFolderName(folder); err != nil {
		return fmt.Errorf("invalid folder name: %w", err)
	}

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Create state directory if needed
	if err := c.tx.Mkdir(ctx, stateDir); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Load existing state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Fetch the database metadata
	database, err := c.client.GetDatabase(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("fetch database: %w", err)
	}

	c.logger.InfoContext(ctx, "found database",
		"title", database.GetTitle(),
		"database_id", databaseID)

	// Query all pages in the database
	dbPages, err := c.client.QueryDatabase(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("query database: %w", err)
	}

	if len(dbPages) == 0 {
		c.logger.InfoContext(ctx, "database is empty")
		return nil
	}

	// Add folder to state
	c.state.AddFolder(folder)

	// Create folder directory
	if err := c.tx.Mkdir(ctx, folder); err != nil {
		return fmt.Errorf("create folder dir: %w", err)
	}

	// Save the database itself as a markdown file (as a root page)
	dbID := normalizePageID(databaseID)
	title := converter.SanitizeFilename(database.GetTitle())
	if title == "" {
		title = defaultUntitledStr
	}

	// Database is always a root page when added directly
	filePath := filepath.Join(folder, title+".md")

	now := time.Now()

	// Convert database to markdown
	content := c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     database.GetTitle(),
		FilePath:      filePath,
		LastSynced:    now,
		NotionType:    "database",
		IsRoot:        true,
		FileProcessor: c.makeFileProcessor(ctx, filePath, dbID),
	})

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write the database file
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return fmt.Errorf("write database: %w", err)
	}

	c.logger.InfoContext(ctx, "downloaded database",
		"database_id", databaseID,
		"title", database.GetTitle(),
		"path", filePath,
		"pages_count", len(dbPages))

	// Collect page IDs to queue
	var pageIDs []string
	for i := range dbPages {
		dbPage := &dbPages[i]
		pageID := normalizePageID(dbPage.ID)
		pageIDs = append(pageIDs, pageID)
		c.logger.DebugContext(ctx, "found database page",
			"page_id", pageID,
			"title", dbPage.Title())
	}

	// Save state
	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Save database registry
	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             dbID,
		Type:           "database",
		Folder:         folder,
		FilePath:       filePath,
		Title:          database.GetTitle(),
		LastEdited:     database.LastEditedTime,
		LastSynced:     now,
		IsRoot:         true,
		ParentID:       "",
		Children:       pageIDs,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Create queue entry for all pages with database as parent
	queueType := queueTypeInit
	if forceUpdate {
		queueType = "update"
	}

	entry := queue.Entry{
		Type:     queueType,
		Folder:   folder,
		PageIDs:  pageIDs,
		ParentID: dbID, // Set database as parent for proper folder structure
	}

	if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
		return fmt.Errorf("create queue entry: %w", err)
	}

	c.logger.InfoContext(ctx, "queued database pages",
		"database", database.GetTitle(),
		"count", len(pageIDs),
		"type", queueType,
		"parent_id", dbID)

	return nil
}

// AddRootPage adds a page as a root page in a folder and queues it for syncing.
//
//nolint:funlen // Complex root page initialization logic
func (c *Crawler) AddRootPage(ctx context.Context, pageID, folder string, forceUpdate bool) error {
	c.logger.InfoContext(ctx, "adding root page",
		"page_id", pageID,
		"folder", folder,
		"force_update", forceUpdate)

	// Validate folder name
	if err := validateFolderName(folder); err != nil {
		return fmt.Errorf("invalid folder name: %w", err)
	}

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Create state directory if needed
	if err := c.tx.Mkdir(ctx, stateDir); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Load existing state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Fetch the page from Notion
	page, err := c.client.GetPage(ctx, pageID)
	if err != nil {
		return fmt.Errorf("fetch page: %w", err)
	}

	// Fetch blocks to get content
	blocks, err := c.client.GetAllBlockChildren(ctx, pageID, 0)
	if err != nil {
		return fmt.Errorf("fetch blocks: %w", err)
	}

	// Add folder to state
	c.state.AddFolder(folder)

	// Compute file path (check for existing path first for stability)
	filePath := c.computeFilePath(ctx, page, folder, true, "")

	// Create folder directory
	if err := c.tx.Mkdir(ctx, folder); err != nil {
		return fmt.Errorf("create folder dir: %w", err)
	}

	now := time.Now()

	// Convert page to markdown
	content := c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     page.Title(),
		FilePath:      filePath,
		LastSynced:    now,
		NotionType:    "page",
		IsRoot:        true,
		FileProcessor: c.makeFileProcessor(ctx, filePath, pageID),
	})

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write the file
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return fmt.Errorf("write page: %w", err)
	}

	c.logger.InfoContext(ctx, "downloaded root page",
		"page_id", pageID,
		"title", page.Title(),
		"path", filePath)

	// Discover child pages
	children := c.findChildPages(blocks)

	// Save state
	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

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
		IsRoot:         true,
		ParentID:       "",
		Children:       children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Create queue entry for child pages
	if len(children) > 0 {
		queueType := queueTypeInit
		if forceUpdate {
			queueType = "update"
		}

		entry := queue.Entry{
			Type:     queueType,
			Folder:   folder,
			PageIDs:  children,
			ParentID: pageID, // Set parent ID for proper folder structure
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			return fmt.Errorf("create queue entry: %w", err)
		}

		c.logger.InfoContext(ctx, "queued child pages",
			"count", len(children),
			"type", queueType,
			"parent_id", pageID)
	}

	return nil
}

// GetPage fetches a single page and places it in the correct location based on its parent hierarchy.
// Unlike AddRootPage, this does not mark the page as a root page.
// If folder is empty, it will be determined from the parent chain.
func (c *Crawler) GetPage(ctx context.Context, pageID string, folder string) error {
	c.logger.InfoContext(ctx, "getting page",
		"page_id", pageID,
		"folder", folder)

	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Create state directory if needed
	if err := c.tx.Mkdir(ctx, stateDir); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Load existing state
	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	// Fetch the page from Notion
	page, err := c.client.GetPage(ctx, pageID)
	if err != nil {
		return fmt.Errorf("fetch page: %w", err)
	}

	// Trace parent chain to find folder and determine hierarchy
	// Note: foundRoot is ignored for add command - we allow adding pages not under a root
	parentChain, targetFolder, _, err := c.traceParentChain(ctx, page, folder)
	if err != nil {
		return fmt.Errorf("trace parent chain: %w", err)
	}

	c.logger.InfoContext(ctx, "traced parent chain",
		"page_id", pageID,
		"folder", targetFolder,
		"missing_parents", len(parentChain))

	// Validate folder name
	if err := validateFolderName(targetFolder); err != nil {
		return fmt.Errorf("invalid folder name: %w", err)
	}

	// Add folder to state
	c.state.AddFolder(targetFolder)

	// Fetch and save all missing parents in the chain (from root to child)
	for i := len(parentChain) - 1; i >= 0; i-- {
		parentPage := parentChain[i]
		if err := c.savePageFromNotion(ctx, parentPage, targetFolder, false); err != nil {
			return fmt.Errorf("save parent page %s: %w", parentPage.ID, err)
		}
	}

	// Now save the requested page
	if err := c.savePageFromNotion(ctx, page, targetFolder, false); err != nil {
		return fmt.Errorf("save page: %w", err)
	}

	// Save state
	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	c.logger.InfoContext(ctx, "page retrieved successfully",
		"page_id", pageID,
		"title", page.Title(),
		"folder", targetFolder)

	return nil
}

// traceParentChain walks up the parent chain until it finds an existing root page or workspace.
// Returns the chain of missing parents (in order from child to root), the folder to use,
// and whether a registered root was found (foundRoot=false means page is not under any root in root.md).
func (c *Crawler) traceParentChain(
	ctx context.Context, page *notion.Page, requestedFolder string,
) ([]*notion.Page, string, bool, error) {
	var missingParents []*notion.Page
	currentPage := page

	for {
		// Get parent ID
		parentID := normalizePageID(currentPage.Parent.ID())
		if parentID == "" {
			// Reached workspace level - no more parents
			break
		}

		// Check if parent exists in registry
		parentReg, err := c.loadPageRegistry(ctx, parentID)
		if err == nil {
			// Found existing parent in registry - verify it's under an enabled root
			enabled, rootID, _ := c.isRootEnabled(ctx, parentID)
			if enabled {
				c.logger.DebugContext(ctx, "found existing parent in registry under enabled root",
					"parent_id", parentID,
					"folder", parentReg.Folder,
					"root_id", rootID)
				return missingParents, parentReg.Folder, true, nil
			}
			// Parent is in registry but not under an enabled root - page is orphaned
			c.logger.DebugContext(ctx, "parent in registry but not under enabled root",
				"parent_id", parentID,
				"root_id", rootID)
			return missingParents, parentReg.Folder, false, nil
		}

		// Parent not in registry - fetch it and add to chain
		parentPage, err := c.client.GetPage(ctx, parentID)
		if err != nil {
			// Check if this is a database
			if strings.Contains(err.Error(), "is a database, not a page") {
				c.logger.DebugContext(ctx, "parent is a database, fetching as database", "parent_id", parentID)
				// For databases, we'll fetch and convert to a page-like structure
				parentPage, err = c.fetchDatabaseAsPage(ctx, parentID)
				if err != nil {
					return nil, "", false, fmt.Errorf("fetch parent database %s: %w", parentID, err)
				}
			} else {
				return nil, "", false, fmt.Errorf("fetch parent page %s: %w", parentID, err)
			}
		}

		missingParents = append(missingParents, parentPage)
		currentPage = parentPage
	}

	// Reached workspace level without finding existing root
	// Use requested folder or default
	targetFolder := requestedFolder
	if targetFolder == "" {
		targetFolder = "default"
	}

	c.logger.DebugContext(ctx, "reached workspace level",
		"folder", targetFolder,
		"missing_parents", len(missingParents))

	return missingParents, targetFolder, false, nil
}

// fetchDatabaseAsPage fetches a database and converts it to a Page structure for parent chain processing.
func (c *Crawler) fetchDatabaseAsPage(ctx context.Context, databaseID string) (*notion.Page, error) {
	database, err := c.client.GetDatabase(ctx, databaseID)
	if err != nil {
		return nil, err
	}

	// Convert database to a page-like structure
	// We create a synthetic Page with the database's metadata
	page := &notion.Page{
		Object:         "page",
		ID:             database.ID,
		CreatedTime:    database.CreatedTime,
		LastEditedTime: database.LastEditedTime,
		CreatedBy:      database.CreatedBy,
		LastEditedBy:   database.LastEditedBy,
		Parent:         database.Parent,
		Archived:       database.Archived,
		InTrash:        database.InTrash,
		URL:            database.URL,
		PublicURL:      database.PublicURL,
	}

	// Properties are not set here as the parent chain processing
	// only needs basic metadata like ID and Parent.

	return page, nil
}

// resolveBlockToPage traces a block's parent chain until it finds a page or database.
// Returns the page/database ID and its type ("page_id" or "database_id").
// If the block chain leads to workspace, returns empty string.
func (c *Crawler) resolveBlockToPage(ctx context.Context, blockID string) (string, string, error) {
	currentID := blockID
	maxDepth := 50 // Prevent infinite loops

	for i := range maxDepth {
		block, err := c.client.GetBlock(ctx, currentID)
		if err != nil {
			return "", "", fmt.Errorf("get block %s: %w", currentID, err)
		}

		switch block.Parent.Type {
		case "page_id":
			c.logger.DebugContext(ctx, "resolved block to page",
				"block_id", blockID,
				"page_id", block.Parent.PageID,
				"depth", i+1)
			return normalizePageID(block.Parent.PageID), "page_id", nil
		case "database_id":
			c.logger.DebugContext(ctx, "resolved block to database",
				"block_id", blockID,
				"database_id", block.Parent.DatabaseID,
				"depth", i+1)
			return normalizePageID(block.Parent.DatabaseID), "database_id", nil
		case parentTypeBlockID:
			// Continue tracing up
			currentID = block.Parent.BlockID
		case parentTypeWorkspace:
			c.logger.DebugContext(ctx, "block chain leads to workspace",
				"block_id", blockID,
				"depth", i+1)
			return "", parentTypeWorkspace, nil
		default:
			return "", "", fmt.Errorf("%w: %s", apperrors.ErrUnexpectedBlockParentType, block.Parent.Type)
		}
	}

	return "", "", apperrors.ErrMaxDepthExceeded
}

// savePageFromNotion fetches blocks and saves a page to the store.
// Handles both regular pages and databases (when parent is a database).
//
//nolint:funlen // Complete page save logic with error handling
func (c *Crawler) savePageFromNotion(ctx context.Context, page *notion.Page, folder string, isRoot bool) error {
	pageID := normalizePageID(page.ID)

	c.logger.DebugContext(ctx, "saving page",
		"page_id", pageID,
		"title", page.Title(),
		"folder", folder,
		"is_root", isRoot)

	// Check if this is actually a database by trying to fetch blocks
	// If it fails with database error, save as database instead
	blocks, err := c.client.GetAllBlockChildren(ctx, pageID, 0)
	if err != nil && strings.Contains(err.Error(), "is a database, not a page") {
		c.logger.DebugContext(ctx, "detected database, saving as database", "page_id", pageID)
		return c.saveDatabaseFromNotion(ctx, pageID, folder, isRoot)
	}
	if err != nil {
		return fmt.Errorf("fetch blocks: %w", err)
	}

	// Determine parent ID first (resolve blocks to containing page)
	parentID := ""
	if page.Parent.Type == parentTypeBlockID {
		resolvedID, resolvedType, err := c.resolveBlockToPage(ctx, page.Parent.BlockID)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to resolve block parent",
				"page_id", pageID,
				"block_id", page.Parent.BlockID,
				"error", err)
		} else if resolvedType != parentTypeWorkspace {
			parentID = resolvedID
		}
	} else {
		parentID = normalizePageID(page.Parent.ID())
	}

	// Compute file path (using resolved parent ID)
	filePath := c.computeFilePath(ctx, page, folder, isRoot, parentID)

	// Create directory if needed
	dir := filepath.Dir(filePath)
	if err := c.tx.Mkdir(ctx, dir); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	now := time.Now()

	// Convert page to markdown
	content := c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     page.Title(),
		FilePath:      filePath,
		LastSynced:    now,
		NotionType:    "page",
		IsRoot:        isRoot,
		ParentID:      parentID,
		FileProcessor: c.makeFileProcessor(ctx, filePath, pageID),
	})

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write the file
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return fmt.Errorf("write page: %w", err)
	}

	c.logger.InfoContext(ctx, "saved page",
		"page_id", pageID,
		"title", page.Title(),
		"path", filePath)

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

	// Queue child pages for later syncing
	if len(children) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  children,
			ParentID: pageID,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue children", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued child pages", "count", len(children))
		}
	}

	return nil
}

// saveDatabaseFromNotion fetches database metadata and pages, then saves as markdown.
//
//nolint:funlen // Complete database save logic with error handling
func (c *Crawler) saveDatabaseFromNotion(ctx context.Context, databaseID, folder string, isRoot bool) error {
	// Fetch the database metadata
	database, err := c.client.GetDatabase(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("fetch database: %w", err)
	}

	// Query all pages in the database
	dbPages, err := c.client.QueryDatabase(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("query database: %w", err)
	}

	dbID := normalizePageID(databaseID)

	// Determine parent ID first (resolve blocks to containing page)
	parentID := ""
	if database.Parent.Type == parentTypeBlockID {
		resolvedID, resolvedType, err := c.resolveBlockToPage(ctx, database.Parent.BlockID)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to resolve block parent",
				"database_id", dbID,
				"block_id", database.Parent.BlockID,
				"error", err)
		} else if resolvedType != parentTypeWorkspace {
			parentID = resolvedID
		}
	} else {
		parentID = normalizePageID(database.Parent.ID())
	}

	// For databases, we use a similar approach to pages
	// Create a synthetic page for path computation
	syntheticPage := &notion.Page{
		ID:             database.ID,
		Parent:         database.Parent,
		LastEditedTime: database.LastEditedTime,
	}

	// Set title property using the proper Property type
	if len(database.Title) > 0 {
		syntheticPage.Properties = notion.Properties{
			"title": {
				Type:  "title",
				Title: database.Title,
			},
		}
	}

	// Compute file path (using resolved parent ID)
	filePath := c.computeFilePath(ctx, syntheticPage, folder, isRoot, parentID)

	// Create directory if needed
	dir := filepath.Dir(filePath)
	if err := c.tx.Mkdir(ctx, dir); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	now := time.Now()

	// Convert database to markdown
	content := c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     database.GetTitle(),
		FilePath:      filePath,
		LastSynced:    now,
		NotionType:    "database",
		IsRoot:        isRoot,
		ParentID:      parentID,
		FileProcessor: c.makeFileProcessor(ctx, filePath, dbID),
	})

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write the file
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return fmt.Errorf("write database: %w", err)
	}

	c.logger.InfoContext(ctx, "saved database",
		"database_id", dbID,
		"title", database.GetTitle(),
		"path", filePath,
		"pages_count", len(dbPages))

	// Collect page IDs (children)
	var children []string
	for i := range dbPages {
		pageID := normalizePageID(dbPages[i].ID)
		children = append(children, pageID)
	}

	// Save page registry
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
		c.logger.WarnContext(ctx, "failed to save database registry", "error", err)
	}

	// Queue database pages for later syncing
	if len(children) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  children,
			ParentID: dbID,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue database pages", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued database pages", "count", len(children))
		}
	}

	return nil
}

// findChildPages extracts child page IDs from blocks.
func (c *Crawler) findChildPages(blocks []notion.Block) []string {
	var children []string
	var traverse func([]notion.Block)

	traverse = func(blocks []notion.Block) {
		for i := range blocks {
			block := &blocks[i]
			if block.Type == "child_page" && block.ChildPage != nil {
				childID := normalizePageID(block.ID)
				if !contains(children, childID) {
					children = append(children, childID)
				}
			}
			if len(block.Children) > 0 {
				traverse(block.Children)
			}
		}
	}

	traverse(blocks)
	return children
}
