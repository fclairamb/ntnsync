package sync

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
)

const (
	rootMdFile = "root.md"
)

// RootEntry represents a row in root.md.
type RootEntry struct {
	Folder  string
	Enabled bool
	URL     string
	PageID  string // Normalized, extracted from URL
}

// RootManifest represents root.md contents.
type RootManifest struct {
	Entries []RootEntry
}

// rootMdTemplate is the default content for a new root.md file.
const rootMdTemplate = `# Root Pages

`

// taskListPattern matches task list entries: - [x] **folder**: url.
var taskListPattern = regexp.MustCompile(`^- \[([ xX])\] \*\*([^*]+)\*\*:\s*(.+)$`)

// ParseRootMd reads and parses root.md from the repository root.
// Returns nil manifest and nil error if the file doesn't exist.
func (c *Crawler) ParseRootMd(ctx context.Context) (*RootManifest, error) {
	data, err := c.store.Read(ctx, rootMdFile)
	if err != nil {
		// File doesn't exist - return nil manifest (not an error condition)
		return nil, nil //nolint:nilerr,nilnil // nil manifest indicates file doesn't exist
	}

	return parseRootMdContent(data)
}

// parseRootMdContent parses the root.md content using task list format.
// Format: - [x] **folder**: url.
func parseRootMdContent(data []byte) (*RootManifest, error) {
	manifest := &RootManifest{}
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		entry, err := parseTaskListEntry(line)
		if err != nil {
			continue // Skip invalid lines
		}
		if entry == nil {
			continue // Line doesn't match pattern
		}

		manifest.Entries = append(manifest.Entries, *entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan root.md: %w", err)
	}

	return manifest, nil
}

// parseTaskListEntry parses a single task list entry from root.md.
// Format: - [x] **folder**: url.
// Returns nil entry (not error) if line doesn't match the pattern.
func parseTaskListEntry(line string) (*RootEntry, error) {
	matches := taskListPattern.FindStringSubmatch(line)
	if matches == nil {
		return nil, nil //nolint:nilnil // nil entry indicates line doesn't match pattern
	}

	checkboxState := matches[1]
	folder := strings.TrimSpace(matches[2])
	url := strings.TrimSpace(matches[3])

	if folder == "" || url == "" {
		return nil, fmt.Errorf("%w: empty folder or url", apperrors.ErrInvalidRootMdRow)
	}

	enabled := checkboxState == "x" || checkboxState == "X"

	// Extract page ID from URL
	pageID, err := notion.ParsePageIDOrURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}

	return &RootEntry{
		Folder:  folder,
		Enabled: enabled,
		URL:     url,
		PageID:  pageID,
	}, nil
}

// WriteRootMd writes the manifest to root.md.
func (c *Crawler) WriteRootMd(ctx context.Context, manifest *RootManifest) error {
	content := formatRootMd(manifest)

	if err := c.tx.Write(ctx, rootMdFile, []byte(content)); err != nil {
		return fmt.Errorf("write root.md: %w", err)
	}

	return nil
}

// formatRootMd formats the manifest as markdown content using task list format.
func formatRootMd(manifest *RootManifest) string {
	var buf bytes.Buffer

	buf.WriteString("# Root Pages\n\n")

	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		checkbox := "[ ]"
		if entry.Enabled {
			checkbox = "[x]"
		}
		buf.WriteString(fmt.Sprintf("- %s **%s**: %s\n", checkbox, entry.Folder, entry.URL))
	}

	return buf.String()
}

// ReconcileRootMd syncs root.md with registries on startup.
// - Creates empty root.md if it doesn't exist
// - Removes duplicates (by page ID)
// - Creates/updates registries to match root.md
// - Queues enabled root pages that haven't been synced yet.
func (c *Crawler) ReconcileRootMd(ctx context.Context) error {
	// Ensure transaction is available
	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	manifest, err := c.ParseRootMd(ctx)
	if err != nil {
		return fmt.Errorf("parse root.md: %w", err)
	}

	// Create empty root.md if it doesn't exist
	if manifest == nil {
		c.logger.InfoContext(ctx, "creating empty root.md")
		if err := c.tx.Write(ctx, rootMdFile, []byte(rootMdTemplate)); err != nil {
			return fmt.Errorf("create root.md: %w", err)
		}
		return nil
	}

	cleaned, pagesToQueue, hasDuplicates := c.processRootEntries(ctx, manifest)

	// Rewrite root.md if duplicates were removed
	if hasDuplicates {
		c.logger.InfoContext(ctx, "rewriting root.md to remove duplicates")
		if err := c.WriteRootMd(ctx, &RootManifest{Entries: cleaned}); err != nil {
			return fmt.Errorf("rewrite root.md: %w", err)
		}
	}

	// Queue enabled root pages that need initial sync
	queuedCount := c.queueRootPages(ctx, pagesToQueue)

	c.logger.InfoContext(ctx, "root.md reconciliation complete",
		"entries", len(cleaned),
		"queued_for_sync", queuedCount)

	return nil
}

// processRootEntries processes root.md entries, creating/updating registries.
// Returns cleaned entries, pages to queue, and whether duplicates were found.
func (c *Crawler) processRootEntries(
	ctx context.Context, manifest *RootManifest,
) ([]RootEntry, map[string][]queue.Page, bool) {
	seenIDs := make(map[string]bool)
	var cleaned []RootEntry
	hasDuplicates := false
	pagesToQueue := make(map[string][]queue.Page)

	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		if seenIDs[entry.PageID] {
			c.logger.WarnContext(ctx, "duplicate page ID in root.md, skipping",
				"page_id", entry.PageID,
				"folder", entry.Folder,
				"url", entry.URL)
			hasDuplicates = true
			continue
		}
		seenIDs[entry.PageID] = true
		cleaned = append(cleaned, *entry)

		needsSync := c.reconcileRootEntry(ctx, entry)

		// Queue enabled root pages that need syncing
		if entry.Enabled && needsSync {
			pagesToQueue[entry.Folder] = append(pagesToQueue[entry.Folder], queue.Page{
				ID:         entry.PageID,
				LastEdited: time.Now(),
			})
		}
	}

	return cleaned, pagesToQueue, hasDuplicates
}

// reconcileRootEntry creates or updates a registry for a root entry.
// Returns true if the page needs to be synced.
func (c *Crawler) reconcileRootEntry(ctx context.Context, entry *RootEntry) bool {
	reg, _ := c.loadPageRegistry(ctx, entry.PageID)
	var needsSync bool

	if reg == nil {
		reg = &PageRegistry{
			ID:      entry.PageID,
			Folder:  entry.Folder,
			IsRoot:  true,
			Enabled: entry.Enabled,
		}
		c.logger.InfoContext(ctx, "creating registry for root page",
			"page_id", entry.PageID,
			"folder", entry.Folder,
			"enabled", entry.Enabled)
		needsSync = true
	} else {
		reg.IsRoot = true
		reg.Enabled = entry.Enabled
		reg.Folder = entry.Folder
		needsSync = reg.LastSynced.IsZero()
	}

	if err := c.savePageRegistry(ctx, reg); err != nil {
		c.logger.WarnContext(ctx, "failed to save registry", "page_id", entry.PageID, "error", err)
	}

	return needsSync
}

// queueRootPages queues root pages for initial sync and returns count of queued pages.
func (c *Crawler) queueRootPages(ctx context.Context, pagesToQueue map[string][]queue.Page) int {
	queuedCount := 0
	for folder, pages := range pagesToQueue {
		entry := queue.Entry{
			Type:      queueTypeInit,
			Folder:    folder,
			Pages:     pages,
			CreatedAt: time.Now(),
		}
		if _, err := c.queueManager.CreateEntry(ctx, entry); err != nil {
			c.logger.WarnContext(ctx, "failed to queue root pages",
				"folder", folder,
				"error", err)
			continue
		}
		queuedCount += len(pages)
		c.logger.InfoContext(ctx, "queued root pages for initial sync",
			"folder", folder,
			"count", len(pages))
	}
	return queuedCount
}

// isRootEnabled traces ancestry to find root, checks if enabled.
// Returns (enabled, rootID, error).
// If the page has no root in root.md, returns (false, "", nil).
func (c *Crawler) isRootEnabled(ctx context.Context, pageID string) (bool, string, error) {
	visited := make(map[string]bool)
	currentID := pageID

	for {
		if visited[currentID] {
			return false, "", apperrors.ErrCycleDetected
		}
		visited[currentID] = true

		reg, err := c.loadPageRegistry(ctx, currentID)
		if err != nil {
			// Registry not found - orphaned page
			return false, "", nil //nolint:nilerr // not finding registry is not an error, just means orphaned
		}
		if reg == nil {
			return false, "", nil
		}

		if reg.IsRoot {
			return reg.Enabled, currentID, nil
		}

		if reg.ParentID == "" {
			// No parent and not a root - orphaned
			return false, "", nil
		}
		currentID = reg.ParentID
	}
}

// GetRootPageIDs returns the page IDs of all roots in root.md.
func (c *Crawler) GetRootPageIDs(ctx context.Context) (map[string]bool, error) {
	manifest, err := c.ParseRootMd(ctx)
	if err != nil {
		return nil, err
	}

	rootIDs := make(map[string]bool)
	if manifest == nil {
		return rootIDs, nil
	}

	for i := range manifest.Entries {
		rootIDs[manifest.Entries[i].PageID] = true
	}

	return rootIDs, nil
}
