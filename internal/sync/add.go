package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/version"
)

// initForAdd validates the folder, ensures a transaction, creates the state directory, and loads state.
func (c *Crawler) initForAdd(ctx context.Context, folder string) error {
	if err := validateFolderName(folder); err != nil {
		return fmt.Errorf("invalid folder name: %w", err)
	}

	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	if err := c.tx.Mkdir(ctx, stateDir); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	if err := c.loadState(ctx); err != nil {
		c.logger.WarnContext(ctx, "could not load state, starting fresh", "error", err)
	}

	return nil
}

// finalizeAddParams holds the parameters for finalizeAdd.
type finalizeAddParams struct {
	itemID      string
	itemType    string // "page" or "database"
	title       string
	folder      string
	filePath    string
	lastEdited  time.Time
	content     []byte
	children    []string
	forceUpdate bool
}

// finalizeAdd handles the shared tail of AddDatabase and AddRootPage:
// add folder to state, mkdir, write file, save state, save registry, queue children.
func (c *Crawler) finalizeAdd(ctx context.Context, params *finalizeAddParams) error {
	c.state.AddFolder(params.folder)

	if err := c.tx.Mkdir(ctx, params.folder); err != nil {
		return fmt.Errorf("create folder dir: %w", err)
	}

	hash := sha256.Sum256(params.content)
	contentHash := hex.EncodeToString(hash[:])

	if err := c.tx.Write(ctx, params.filePath, params.content); err != nil {
		return fmt.Errorf("write %s: %w", params.itemType, err)
	}

	logKey := params.itemType + "_id"
	c.logger.InfoContext(ctx, "downloaded "+params.itemType,
		logKey, params.itemID,
		"title", params.title,
		"path", params.filePath)

	if err := c.saveState(ctx); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	now := time.Now()

	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             params.itemID,
		Type:           params.itemType,
		Folder:         params.folder,
		FilePath:       params.filePath,
		Title:          params.title,
		LastEdited:     params.lastEdited,
		LastSynced:     now,
		IsRoot:         true,
		Enabled:        true,
		ParentID:       "",
		Children:       params.children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	if len(params.children) > 0 {
		queueType := queueTypeInit
		if params.forceUpdate {
			queueType = "update"
		}

		entry := queue.Entry{
			Type:     queueType,
			Folder:   params.folder,
			PageIDs:  params.children,
			ParentID: params.itemID,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			return fmt.Errorf("create queue entry: %w", err)
		}

		c.logger.InfoContext(ctx, "queued child "+params.itemType+"s",
			"count", len(params.children),
			"type", queueType,
			"parent_id", params.itemID)
	}

	return nil
}

// AddDatabase adds all pages from a database to a folder.
func (c *Crawler) AddDatabase(ctx context.Context, databaseID, folder string, forceUpdate bool) error {
	c.logger.InfoContext(ctx, "adding database",
		"database_id", databaseID,
		"folder", folder,
		"force_update", forceUpdate)

	if err := c.initForAdd(ctx, folder); err != nil {
		return err
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

	dbID := normalizePageID(databaseID)
	title := converter.SanitizeFilename(database.GetTitle())
	if title == "" {
		title = defaultUntitledStr
	}

	filePath := filepath.Join(folder, title+".md")

	content := c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     database.GetTitle(),
		FilePath:      filePath,
		LastSynced:    time.Now(),
		NotionType:    "database",
		IsRoot:        true,
		FileProcessor: c.makeFileProcessor(ctx, filePath, dbID),
	})

	var children []string
	for i := range dbPages {
		dbPage := &dbPages[i]
		pageID := normalizePageID(dbPage.ID)
		children = append(children, pageID)
		c.logger.DebugContext(ctx, "found database page",
			"page_id", pageID,
			"title", dbPage.Title())
	}

	return c.finalizeAdd(ctx, &finalizeAddParams{
		itemID:      dbID,
		itemType:    "database",
		title:       database.GetTitle(),
		folder:      folder,
		filePath:    filePath,
		lastEdited:  database.LastEditedTime,
		content:     content,
		children:    children,
		forceUpdate: forceUpdate,
	})
}

// AddRootPage adds a page as a root page in a folder and queues it for syncing.
func (c *Crawler) AddRootPage(ctx context.Context, pageID, folder string, forceUpdate bool) error {
	c.logger.InfoContext(ctx, "adding root page",
		"page_id", pageID,
		"folder", folder,
		"force_update", forceUpdate)

	if err := c.initForAdd(ctx, folder); err != nil {
		return err
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

	filePath := c.computeFilePath(ctx, page, folder, true, "")

	content := c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     page.Title(),
		FilePath:      filePath,
		LastSynced:    time.Now(),
		NotionType:    "page",
		IsRoot:        true,
		FileProcessor: c.makeFileProcessor(ctx, filePath, pageID),
	})

	children := c.findChildPages(blocks)

	return c.finalizeAdd(ctx, &finalizeAddParams{
		itemID:      pageID,
		itemType:    "page",
		title:       page.Title(),
		folder:      folder,
		filePath:    filePath,
		lastEdited:  page.LastEditedTime,
		content:     content,
		children:    children,
		forceUpdate: forceUpdate,
	})
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

	// Trace parent chain to find folder and determine hierarchy.
	// The foundRoot return value is ignored here since the add command allows adding pages not under a root.
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

// resolveParentID resolves the parent ID from a notion.Parent, handling block parents.
func (c *Crawler) resolveParentID(ctx context.Context, itemID, logKey string, parent notion.Parent) string {
	if parent.Type == parentTypeBlockID {
		resolvedID, resolvedType, err := c.resolveBlockToPage(ctx, parent.BlockID)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to resolve block parent",
				logKey, itemID,
				"block_id", parent.BlockID,
				"error", err)
			return ""
		}
		if resolvedType == parentTypeWorkspace {
			return ""
		}
		return resolvedID
	}
	return normalizePageID(parent.ID())
}

// writeRegistryAndQueue writes content to a file, saves the page registry, and queues children.
func (c *Crawler) writeRegistryAndQueue(
	ctx context.Context, filePath, itemID, itemType, title, folder, parentID string,
	lastEdited time.Time, isRoot bool, content []byte, children []string,
) error {
	// Create directory if needed
	dir := filepath.Dir(filePath)
	if err := c.tx.Mkdir(ctx, dir); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	// Compute content hash
	hash := sha256.Sum256(content)
	contentHash := hex.EncodeToString(hash[:])

	// Write the file
	if err := c.tx.Write(ctx, filePath, content); err != nil {
		return fmt.Errorf("write %s: %w", itemType, err)
	}

	logKey := itemType + "_id"
	c.logger.InfoContext(ctx, "saved "+itemType,
		logKey, itemID,
		"title", title,
		"path", filePath)

	now := time.Now()

	// Save page registry
	if err := c.savePageRegistry(ctx, &PageRegistry{
		NtnsyncVersion: version.Version,
		ID:             itemID,
		Type:           itemType,
		Folder:         folder,
		FilePath:       filePath,
		Title:          title,
		LastEdited:     lastEdited,
		LastSynced:     now,
		IsRoot:         isRoot,
		ParentID:       parentID,
		Children:       children,
		ContentHash:    contentHash,
	}); err != nil {
		c.logger.WarnContext(ctx, "failed to save page registry", "error", err)
	}

	// Queue children for later syncing
	if len(children) > 0 {
		entry := queue.Entry{
			Type:     queueTypeInit,
			Folder:   folder,
			PageIDs:  children,
			ParentID: itemID,
		}

		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue child pages", "error", err)
		} else {
			c.logger.DebugContext(ctx, "queued child pages", "count", len(children))
		}
	}

	return nil
}

// savePageFromNotion fetches blocks and saves a page to the store.
// Handles both regular pages and databases (when parent is a database).
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

		database, dbErr := c.client.GetDatabase(ctx, pageID)
		if dbErr != nil {
			return fmt.Errorf("fetch database: %w", dbErr)
		}

		dbPages, dbErr := c.client.QueryDatabase(ctx, pageID)
		if dbErr != nil {
			return fmt.Errorf("query database: %w", dbErr)
		}

		parentID := c.resolveParentID(ctx, pageID, "database_id", database.Parent)
		syntheticPage := &notion.Page{
			ID:     database.ID,
			Parent: database.Parent,
			Properties: notion.Properties{
				"title": {Type: "title", Title: database.Title},
			},
		}
		filePath := c.computeFilePath(ctx, syntheticPage, folder, isRoot, parentID)

		content := c.converter.ConvertDatabase(database, dbPages, &converter.ConvertOptions{
			Folder:        folder,
			PageTitle:     database.GetTitle(),
			FilePath:      filePath,
			LastSynced:    time.Now(),
			NotionType:    "database",
			IsRoot:        isRoot,
			ParentID:      parentID,
			FileProcessor: c.makeFileProcessor(ctx, filePath, pageID),
		})

		var children []string
		for i := range dbPages {
			children = append(children, normalizePageID(dbPages[i].ID))
		}

		return c.writeRegistryAndQueue(ctx, filePath, pageID, "database",
			database.GetTitle(), folder, parentID, database.LastEditedTime, isRoot, content, children)
	}
	if err != nil {
		return fmt.Errorf("fetch blocks: %w", err)
	}

	parentID := c.resolveParentID(ctx, pageID, "page_id", page.Parent)
	filePath := c.computeFilePath(ctx, page, folder, isRoot, parentID)

	content := c.converter.ConvertWithOptions(page, blocks, &converter.ConvertOptions{
		Folder:        folder,
		PageTitle:     page.Title(),
		FilePath:      filePath,
		LastSynced:    time.Now(),
		NotionType:    "page",
		IsRoot:        isRoot,
		ParentID:      parentID,
		FileProcessor: c.makeFileProcessor(ctx, filePath, pageID),
	})

	children := c.findChildPages(blocks)

	return c.writeRegistryAndQueue(ctx, filePath, pageID, "page",
		page.Title(), folder, parentID, page.LastEditedTime, isRoot, content, children)
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
				if !slices.Contains(children, childID) {
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
