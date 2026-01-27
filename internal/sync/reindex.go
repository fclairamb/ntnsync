package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/store"
)

// reindexResult holds the intermediate results of reindexing.
type reindexResult struct {
	registryMap   map[string]*PageRegistry // key: notion_id
	filesToDelete []string
	duplicates    []string
}

// Reindex rebuilds the registry from markdown files.
func (c *Crawler) Reindex(ctx context.Context, dryRun bool) error {
	c.logger.InfoContext(ctx, "reindexing", "dry_run", dryRun)

	// Ensure transaction is available (for saving registries)
	if !dryRun {
		if err := c.EnsureTransaction(ctx); err != nil {
			return fmt.Errorf("ensure transaction: %w", err)
		}
	}

	// Find all markdown files
	c.logger.InfoContext(ctx, "scanning for markdown files")
	mdFiles, err := c.findMarkdownFiles(ctx, ".")
	if err != nil {
		return fmt.Errorf("find markdown files: %w", err)
	}

	c.logger.InfoContext(ctx, "found markdown files", "count", len(mdFiles))

	// Parse and analyze files
	result := c.analyzeMarkdownFiles(ctx, mdFiles)

	// Summary
	c.logger.InfoContext(ctx, "reindex summary",
		"total_files", len(mdFiles),
		"unique_pages", len(result.registryMap),
		"duplicates", len(result.duplicates),
		"files_to_delete", len(result.filesToDelete))

	if dryRun {
		c.logger.InfoContext(ctx, "dry run - no changes made")
		return nil
	}

	// Save all registries
	if err := c.saveRegistries(ctx, result.registryMap); err != nil {
		return err
	}

	// Delete duplicate files
	if len(result.filesToDelete) > 0 {
		if err := c.deleteDuplicateFiles(ctx, result.filesToDelete); err != nil {
			return err
		}
	}

	c.logger.InfoContext(ctx, "reindex complete")
	return nil
}

// analyzeMarkdownFiles parses files and detects duplicates.
func (c *Crawler) analyzeMarkdownFiles(ctx context.Context, mdFiles []string) *reindexResult {
	registryMap := make(map[string]*PageRegistry)
	filesByID := make(map[string][]string)

	// Parse each file
	for _, filePath := range mdFiles {
		reg, err := c.parseRegistryFromFile(ctx, filePath)
		if err != nil {
			c.logger.WarnContext(ctx, "failed to parse file", "path", filePath, "error", err)
			continue
		}

		if reg.ID == "" {
			c.logger.WarnContext(ctx, "skipping file without notion_id", "path", filePath)
			continue
		}

		filesByID[reg.ID] = append(filesByID[reg.ID], filePath)
		c.updateRegistryMap(registryMap, reg)
	}

	// Detect duplicates
	duplicates, filesToDelete := c.detectDuplicates(ctx, filesByID, registryMap)

	return &reindexResult{
		registryMap:   registryMap,
		filesToDelete: filesToDelete,
		duplicates:    duplicates,
	}
}

// updateRegistryMap keeps the registry with the latest last_edited time.
func (c *Crawler) updateRegistryMap(registryMap map[string]*PageRegistry, reg *PageRegistry) {
	if existing, exists := registryMap[reg.ID]; exists {
		if reg.LastEdited.After(existing.LastEdited) {
			registryMap[reg.ID] = reg
		}
	} else {
		registryMap[reg.ID] = reg
	}
}

// detectDuplicates finds files with duplicate notion_ids.
func (c *Crawler) detectDuplicates(
	ctx context.Context,
	filesByID map[string][]string,
	registryMap map[string]*PageRegistry,
) ([]string, []string) {
	var duplicates []string
	var filesToDelete []string

	for pageID, files := range filesByID {
		if len(files) <= 1 {
			continue
		}

		duplicates = append(duplicates, pageID)
		keepFile := registryMap[pageID].FilePath

		c.logger.WarnContext(ctx, "duplicate notion_id found",
			"notion_id", pageID,
			"file_count", len(files),
			"keeping", keepFile)

		for _, f := range files {
			if f != keepFile {
				filesToDelete = append(filesToDelete, f)
				c.logger.InfoContext(ctx, "will delete duplicate", "path", f)
			}
		}
	}

	return duplicates, filesToDelete
}

// saveRegistries saves all registries to disk.
func (c *Crawler) saveRegistries(ctx context.Context, registryMap map[string]*PageRegistry) error {
	for _, reg := range registryMap {
		if err := c.savePageRegistry(ctx, reg); err != nil {
			c.logger.ErrorContext(ctx, "failed to save registry",
				"notion_id", reg.ID,
				"error", err)
			return fmt.Errorf("save registry %s: %w", reg.ID, err)
		}
		c.logger.DebugContext(ctx, "saved registry", "notion_id", reg.ID, "path", reg.FilePath)
	}
	return nil
}

// deleteDuplicateFiles deletes duplicate files in a transaction.
func (c *Crawler) deleteDuplicateFiles(ctx context.Context, filesToDelete []string) error {
	transaction, err := c.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	for _, filePath := range filesToDelete {
		if delErr := transaction.Delete(ctx, filePath); delErr != nil {
			c.logger.ErrorContext(ctx, "failed to delete duplicate", "path", filePath, "error", delErr)
			if rbErr := transaction.Rollback(ctx); rbErr != nil {
				c.logger.ErrorContext(ctx, "rollback failed", "error", rbErr)
			}
			return fmt.Errorf("delete %s: %w", filePath, delErr)
		}
	}

	if err := transaction.Commit(ctx, "reindex: remove duplicate files"); err != nil {
		// Ignore "empty commit" errors - this happens when deleted files weren't tracked in git
		if !strings.Contains(err.Error(), "empty commit") && !strings.Contains(err.Error(), "clean working tree") {
			return fmt.Errorf("commit: %w", err)
		}
		c.logger.DebugContext(ctx, "no git changes to commit (files were not tracked)")
	}

	return nil
}

// findMarkdownFiles recursively finds all .md files, excluding .notion-sync directory.
func (c *Crawler) findMarkdownFiles(ctx context.Context, rootDir string) ([]string, error) {
	var mdFiles []string

	var walkDir func(string) error
	walkDir = func(dir string) error {
		entries, err := c.store.List(ctx, dir)
		if err != nil {
			return err
		}

		for i := range entries {
			entry := &entries[i]
			if c.shouldSkipDirectory(entry) {
				continue
			}

			if entry.IsDir {
				if err := walkDir(entry.Path); err != nil {
					return err
				}
			} else if strings.HasSuffix(entry.Path, ".md") {
				mdFiles = append(mdFiles, entry.Path)
			}
		}

		return nil
	}

	if err := walkDir(rootDir); err != nil {
		return nil, err
	}

	return mdFiles, nil
}

// shouldSkipDirectory returns true if the directory should be skipped during file walking.
func (c *Crawler) shouldSkipDirectory(entry *store.FileInfo) bool {
	if !entry.IsDir {
		return false
	}
	baseName := filepath.Base(entry.Path)
	return baseName == stateDir || strings.HasPrefix(baseName, ".")
}

// parseRegistryFromFile extracts PageRegistry information from a markdown file's frontmatter.
func (c *Crawler) parseRegistryFromFile(ctx context.Context, filePath string) (*PageRegistry, error) {
	content, err := c.store.Read(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	endIdx, err := c.findFrontmatterEnd(lines)
	if err != nil {
		return nil, err
	}

	reg := &PageRegistry{FilePath: filePath}
	c.parseFrontmatterFields(lines, endIdx, reg)
	c.extractTitle(lines, endIdx, filePath, reg)

	return reg, nil
}

// findFrontmatterEnd finds the closing --- of frontmatter.
func (c *Crawler) findFrontmatterEnd(lines []string) (int, error) {
	if len(lines) < 3 || lines[0] != "---" {
		return -1, apperrors.ErrNoFrontmatter
	}

	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return i, nil
		}
	}

	return -1, apperrors.ErrFrontmatterNotClosed
}

// parseFrontmatterFields parses the frontmatter fields into a registry.
func (c *Crawler) parseFrontmatterFields(lines []string, endIdx int, reg *PageRegistry) {
	for i := 1; i < endIdx; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", frontmatterFieldCount)
		if len(parts) != frontmatterFieldCount {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		c.setRegistryField(reg, key, value)
	}
}

// setRegistryField sets a single field on the registry based on key-value pair.
func (c *Crawler) setRegistryField(reg *PageRegistry, key, value string) {
	switch key {
	case "notion_id":
		reg.ID = normalizePageID(value)
	case "notion_type":
		reg.Type = value
	case "notion_folder":
		reg.Folder = value
	case "file_path":
		if value != "" {
			reg.FilePath = value
		}
	case "last_edited":
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			reg.LastEdited = t
		}
	case "last_synced":
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			reg.LastSynced = t
		}
	case "is_root":
		reg.IsRoot = value == "true"
	case "notion_parent_id":
		reg.ParentID = normalizePageID(value)
	}
}

// extractTitle extracts the title from the first heading or uses the filename.
func (c *Crawler) extractTitle(lines []string, endIdx int, filePath string, reg *PageRegistry) {
	for i := endIdx + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			reg.Title = strings.TrimSpace(strings.TrimLeft(line, "#"))
			return
		}
		break // Stop at first non-empty, non-heading line
	}

	// Use filename as title if no heading found
	base := filepath.Base(filePath)
	reg.Title = strings.TrimSuffix(base, ".md")
}

// CommitChanges commits pending changes to git.
func (c *Crawler) CommitChanges(ctx context.Context, message string) error {
	c.logger.InfoContext(ctx, "committing changes", "message", message)

	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := tx.Commit(ctx, message); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	c.logger.InfoContext(ctx, "changes committed")
	return nil
}
