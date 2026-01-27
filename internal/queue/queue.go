// Package queue provides queue management for Notion page synchronization.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/store"
)

const (
	queueDir           = ".notion-sync/queue"
	queueFileFormat    = "%08d.json" // 00000001.json, 00000002.json, etc.
	maxItemsPerQueue   = 10          // Maximum page IDs per queue file
	webhookIDThreshold = 1000        // IDs below this are for webhook events (high priority)
)

// Page represents a page in the queue with its last edited time.
type Page struct {
	ID         string    `json:"id"`          // Page ID
	LastEdited time.Time `json:"last_edited"` // Last edited time from Notion
}

// Entry represents a single queue file's content.
type Entry struct {
	Type      string    `json:"type"`               // "init" or "update"
	Folder    string    `json:"folder"`             // Folder name
	Pages     []Page    `json:"pages,omitempty"`    // Pages to process (new format)
	PageIDs   []string  `json:"pageIds,omitempty"`  // Page IDs to process (legacy format, deprecated)
	ParentID  string    `json:"parentId,omitempty"` // Parent page ID (for child pages)
	CreatedAt time.Time `json:"createdAt"`          // When this queue entry was created
}

// GetPageIDs returns all page IDs from the entry, supporting both old and new formats.
func (qe *Entry) GetPageIDs() []string {
	if len(qe.Pages) > 0 {
		ids := make([]string, len(qe.Pages))
		for i := range qe.Pages {
			ids[i] = qe.Pages[i].ID
		}
		return ids
	}
	return qe.PageIDs
}

// GetPageCount returns the number of pages in the entry.
func (qe *Entry) GetPageCount() int {
	if len(qe.Pages) > 0 {
		return len(qe.Pages)
	}
	return len(qe.PageIDs)
}

// Manager handles queue file operations.
type Manager struct {
	store  store.Store
	tx     store.Transaction
	Logger *slog.Logger
}

// NewManager creates a queue manager.
func NewManager(st store.Store, logger *slog.Logger) *Manager {
	return &Manager{
		store:  st,
		Logger: logger,
	}
}

// SetTransaction sets the transaction to use for write operations.
func (qm *Manager) SetTransaction(tx store.Transaction) {
	qm.tx = tx
}

// CreateEntry creates new queue file(s) with the next sequential number(s).
// If entry has more than maxItemsPerQueue pages, it splits into multiple files.
func (qm *Manager) CreateEntry(ctx context.Context, entry Entry) (string, error) {
	// Determine if we're using new or legacy format
	useNewFormat := len(entry.Pages) > 0
	pageCount := entry.GetPageCount()

	if pageCount == 0 {
		return "", nil // Nothing to queue
	}

	if useNewFormat {
		return qm.createEntriesNewFormat(ctx, entry)
	}
	return qm.createEntriesLegacyFormat(ctx, entry)
}

// ListEntries returns all queue files in sorted order.
func (qm *Manager) ListEntries(ctx context.Context) ([]string, error) {
	// Read queue directory
	entries, err := qm.store.List(ctx, queueDir)
	if err != nil {
		// If directory doesn't exist, return empty list
		return nil, err
	}

	// Filter for .json files and extract filenames
	var queueFiles []string
	for i := range entries {
		entry := &entries[i]
		if !entry.IsDir && strings.HasSuffix(entry.Path, ".json") {
			// Extract just the filename from the path
			filename := filepath.Base(entry.Path)
			queueFiles = append(queueFiles, filename)
		}
	}

	sort.Strings(queueFiles)
	return queueFiles, nil
}

// ReadEntry reads a queue file.
func (qm *Manager) ReadEntry(ctx context.Context, filename string) (*Entry, error) {
	path := filepath.Join(queueDir, filename)
	data, err := qm.store.Read(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read queue file: %w", err)
	}

	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal entry: %w", err)
	}

	return &entry, nil
}

// UpdateEntry updates a queue file (typically to remove processed pages).
func (qm *Manager) UpdateEntry(ctx context.Context, filename string, entry *Entry) error {
	qm.Logger.DebugContext(ctx, "updating queue entry",
		"filename", filename,
		"remaining_pages", entry.GetPageCount())

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	path := filepath.Join(queueDir, filename)
	if err := qm.tx.Write(path, data); err != nil {
		return fmt.Errorf("write queue file: %w", err)
	}

	return nil
}

// DeleteEntry removes an empty queue file.
func (qm *Manager) DeleteEntry(ctx context.Context, filename string) error {
	qm.Logger.DebugContext(ctx, "deleting queue entry", "filename", filename)

	path := filepath.Join(queueDir, filename)
	if err := qm.tx.Delete(path); err != nil {
		return fmt.Errorf("delete queue file: %w", err)
	}

	return nil
}

// IsPageQueued checks if a page is already queued.
// For type "init", this is used for deduplication.
// For type "update", duplicates are allowed.
func (qm *Manager) IsPageQueued(ctx context.Context, pageID, queueType string) (bool, error) {
	files, err := qm.ListEntries(ctx)
	if err != nil {
		return false, err
	}

	for _, filename := range files {
		entry, err := qm.ReadEntry(ctx, filename)
		if err != nil {
			qm.Logger.WarnContext(ctx, "failed to read queue entry",
				"filename", filename,
				"error", err)
			continue
		}

		// Only check entries of the same type
		if entry.Type != queueType {
			continue
		}

		// Check if page ID is in this entry
		if slices.Contains(entry.GetPageIDs(), pageID) {
			return true, nil
		}
	}

	return false, nil
}

// GetNextQueueNumber returns the next available queue file number for regular (non-webhook) entries.
// Regular entries start at webhookIDThreshold (1000) and increment upward.
func (qm *Manager) GetNextQueueNumber(ctx context.Context) (int, error) {
	files, err := qm.ListEntries(ctx)
	if err != nil {
		return 0, err
	}

	if len(files) == 0 {
		return webhookIDThreshold, nil
	}

	// Find the maximum ID >= webhookIDThreshold
	maxNum := webhookIDThreshold - 1
	for _, file := range files {
		numStr := strings.TrimSuffix(file, ".json")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		if num >= webhookIDThreshold && num > maxNum {
			maxNum = num
		}
	}

	return maxNum + 1, nil
}

// GetMinQueueID returns the minimum queue ID from existing entries.
// Returns 0 if there are no entries.
func (qm *Manager) GetMinQueueID(ctx context.Context) (int, error) {
	files, err := qm.ListEntries(ctx)
	if err != nil || len(files) == 0 {
		return 0, err
	}

	numStr := strings.TrimSuffix(files[0], ".json")
	return strconv.Atoi(numStr)
}

// CreateWebhookEntry creates a queue entry for webhook-triggered events.
// Webhook entries use IDs below webhookIDThreshold (decrementing from 999, 998, ...)
// to ensure they are processed before regular queue entries.
func (qm *Manager) CreateWebhookEntry(ctx context.Context, pageID, folder string) (string, error) {
	// Find the current minimum queue ID
	minID, err := qm.GetMinQueueID(ctx)
	if err != nil {
		return "", fmt.Errorf("get min queue id: %w", err)
	}

	// Determine the new ID
	var newID int
	if minID == 0 || minID >= webhookIDThreshold {
		// No webhook entries yet, start at 999
		newID = webhookIDThreshold - 1
	} else {
		// Decrement from current minimum
		newID = minID - 1
	}

	filename := fmt.Sprintf(queueFileFormat, newID)
	qm.Logger.DebugContext(ctx, "creating webhook queue entry",
		"filename", filename,
		"page_id", pageID,
		"folder", folder)

	// Create entry with type "update" (webhook events always force sync)
	entry := Entry{
		Type:   "update",
		Folder: folder,
		Pages: []Page{
			{ID: pageID, LastEdited: time.Now()},
		},
		CreatedAt: time.Now(),
	}

	// Marshal entry
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}

	// Write queue file
	path := filepath.Join(queueDir, filename)
	if err := qm.tx.Write(path, data); err != nil {
		return "", fmt.Errorf("write queue file: %w", err)
	}

	return filename, nil
}

// createEntriesNewFormat creates queue entries using the new Pages format.
func (qm *Manager) createEntriesNewFormat(ctx context.Context, entry Entry) (string, error) {
	var firstFilename string
	pages := entry.Pages

	for len(pages) > 0 {
		// Take up to maxItemsPerQueue items
		chunkSize := min(len(pages), maxItemsPerQueue)
		chunk := pages[:chunkSize]
		pages = pages[chunkSize:]

		filename, err := qm.createChunkEntryNewFormat(ctx, entry, chunk)
		if err != nil {
			return "", err
		}

		if firstFilename == "" {
			firstFilename = filename
		}
	}

	return firstFilename, nil
}

// createEntriesLegacyFormat creates queue entries using the legacy PageIDs format.
func (qm *Manager) createEntriesLegacyFormat(ctx context.Context, entry Entry) (string, error) {
	var firstFilename string
	pageIDs := entry.PageIDs

	for len(pageIDs) > 0 {
		// Take up to maxItemsPerQueue items
		chunkSize := min(len(pageIDs), maxItemsPerQueue)
		chunk := pageIDs[:chunkSize]
		pageIDs = pageIDs[chunkSize:]

		filename, err := qm.createChunkEntryLegacy(ctx, entry, chunk)
		if err != nil {
			return "", err
		}

		if firstFilename == "" {
			firstFilename = filename
		}
	}

	return firstFilename, nil
}

// createChunkEntryNewFormat creates a single queue file for a chunk using new format.
func (qm *Manager) createChunkEntryNewFormat(
	ctx context.Context, entry Entry, chunk []Page,
) (string, error) {
	nextNum, err := qm.GetNextQueueNumber(ctx)
	if err != nil {
		return "", fmt.Errorf("get next queue number: %w", err)
	}

	filename := fmt.Sprintf(queueFileFormat, nextNum)
	qm.Logger.DebugContext(ctx, "creating queue entry",
		"filename", filename,
		"type", entry.Type,
		"folder", entry.Folder,
		"page_count", len(chunk))

	chunkEntry := Entry{
		Type:      entry.Type,
		Folder:    entry.Folder,
		Pages:     chunk,
		ParentID:  entry.ParentID,
		CreatedAt: time.Now(),
	}

	data, err := json.MarshalIndent(chunkEntry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}

	path := filepath.Join(queueDir, filename)
	if err := qm.tx.Write(path, data); err != nil {
		return "", fmt.Errorf("write queue file: %w", err)
	}

	return filename, nil
}

// createChunkEntryLegacy creates a single queue file for a chunk using legacy format.
func (qm *Manager) createChunkEntryLegacy(ctx context.Context, entry Entry, chunk []string) (string, error) {
	nextNum, err := qm.GetNextQueueNumber(ctx)
	if err != nil {
		return "", fmt.Errorf("get next queue number: %w", err)
	}

	filename := fmt.Sprintf(queueFileFormat, nextNum)
	qm.Logger.DebugContext(ctx, "creating queue entry",
		"filename", filename,
		"type", entry.Type,
		"folder", entry.Folder,
		"page_count", len(chunk))

	chunkEntry := Entry{
		Type:      entry.Type,
		Folder:    entry.Folder,
		PageIDs:   chunk,
		ParentID:  entry.ParentID,
		CreatedAt: time.Now(),
	}

	data, err := json.MarshalIndent(chunkEntry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal entry: %w", err)
	}

	path := filepath.Join(queueDir, filename)
	if err := qm.tx.Write(path, data); err != nil {
		return "", fmt.Errorf("write queue file: %w", err)
	}

	return filename, nil
}
