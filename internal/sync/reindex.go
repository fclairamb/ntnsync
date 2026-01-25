package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// Reindex rebuilds the registry from markdown files.
//
//nolint:funlen // Complete reindexing logic with file walking
func (c *Crawler) Reindex(ctx context.Context, dryRun bool) error {
	c.logger.InfoContext(ctx, "reindexing", "dry_run", dryRun)

	// Find all markdown files
	c.logger.InfoContext(ctx, "scanning for markdown files")
	mdFiles, err := c.findMarkdownFiles(ctx, ".")
	if err != nil {
		return fmt.Errorf("find markdown files: %w", err)
	}

	c.logger.InfoContext(ctx, "found markdown files", "count", len(mdFiles))

	// Parse frontmatter from each file
	registryMap := make(map[string]*PageRegistry) // key: notion_id
	filesByID := make(map[string][]string)        // key: notion_id, value: file paths

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

		// Track all files with this notion_id
		filesByID[reg.ID] = append(filesByID[reg.ID], filePath)

		// Keep the registry with the latest last_edited time
		if existing, exists := registryMap[reg.ID]; exists {
			if reg.LastEdited.After(existing.LastEdited) {
				registryMap[reg.ID] = reg
			}
		} else {
			registryMap[reg.ID] = reg
		}
	}

	// Log duplicates
	var duplicates []string
	var filesToDelete []string
	for pageID, files := range filesByID {
		if len(files) > 1 {
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
	}

	// Summary
	c.logger.InfoContext(ctx, "reindex summary",
		"total_files", len(mdFiles),
		"unique_pages", len(registryMap),
		"duplicates", len(duplicates),
		"files_to_delete", len(filesToDelete))

	if dryRun {
		c.logger.InfoContext(ctx, "dry run - no changes made")
		return nil
	}

	// Save all registries
	for _, reg := range registryMap {
		if err := c.savePageRegistry(ctx, reg); err != nil {
			c.logger.ErrorContext(ctx, "failed to save registry",
				"notion_id", reg.ID,
				"error", err)
			return fmt.Errorf("save registry %s: %w", reg.ID, err)
		}
		c.logger.DebugContext(ctx, "saved registry", "notion_id", reg.ID, "path", reg.FilePath)
	}

	// Delete duplicate files
	if len(filesToDelete) > 0 {
		if err := c.deleteDuplicateFiles(ctx, filesToDelete); err != nil {
			return err
		}
	}

	c.logger.InfoContext(ctx, "reindex complete")
	return nil
}

// deleteDuplicateFiles deletes duplicate files in a transaction.
func (c *Crawler) deleteDuplicateFiles(ctx context.Context, filesToDelete []string) error {
	transaction, err := c.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	for _, filePath := range filesToDelete {
		if delErr := transaction.Delete(filePath); delErr != nil {
			c.logger.ErrorContext(ctx, "failed to delete duplicate", "path", filePath, "error", delErr)
			if rbErr := transaction.Rollback(); rbErr != nil {
				c.logger.ErrorContext(ctx, "rollback failed", "error", rbErr)
			}
			return fmt.Errorf("delete %s: %w", filePath, delErr)
		}
	}

	if err := transaction.Commit("reindex: remove duplicate files"); err != nil {
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
			// Skip .notion-sync directory
			if entry.IsDir && filepath.Base(entry.Path) == stateDir {
				continue
			}

			// Skip hidden directories (starting with .)
			if entry.IsDir && strings.HasPrefix(filepath.Base(entry.Path), ".") {
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

// parseRegistryFromFile extracts PageRegistry information from a markdown file's frontmatter.
//
//nolint:funlen // Complete frontmatter parsing with all field extraction
func (c *Crawler) parseRegistryFromFile(ctx context.Context, filePath string) (*PageRegistry, error) {
	content, err := c.store.Read(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	// Extract frontmatter
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return nil, apperrors.ErrNoFrontmatter
	}

	// Find closing ---
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			endIdx = i
			break
		}
	}

	if endIdx == -1 {
		return nil, apperrors.ErrFrontmatterNotClosed
	}

	// Parse frontmatter fields
	reg := &PageRegistry{
		FilePath: filePath,
	}

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

		switch key {
		case "notion_id":
			reg.ID = normalizePageID(value)
		case "notion_type":
			reg.Type = value
		case "notion_folder":
			reg.Folder = value
		case "file_path":
			// Use file_path from frontmatter if available, otherwise use actual file path
			if value != "" {
				reg.FilePath = value
			}
		case "last_edited":
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				reg.LastEdited = t
			}
		case "last_synced":
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				reg.LastSynced = t
			}
		case "is_root":
			reg.IsRoot = value == "true"
		case "notion_parent_id":
			reg.ParentID = normalizePageID(value)
		}
	}

	// Extract title from first heading if available
	for i := endIdx + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			// Extract title from markdown heading
			// Remove all leading # characters and spaces
			reg.Title = strings.TrimSpace(strings.TrimLeft(line, "#"))
			break
		}
	}

	// If no title found in heading, use filename
	if reg.Title == "" {
		base := filepath.Base(filePath)
		reg.Title = strings.TrimSuffix(base, ".md")
	}

	return reg, nil
}

// CommitChanges commits pending changes to git.
func (c *Crawler) CommitChanges(ctx context.Context, message string) error {
	c.logger.InfoContext(ctx, "committing changes", "message", message)

	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := tx.Commit(message); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	c.logger.InfoContext(ctx, "changes committed")
	return nil
}
