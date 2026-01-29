package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/version"
)

// Size constants.
const (
	// Byte size multipliers.
	bytesPerKB = 1024
	bytesPerMB = 1024 * 1024
	bytesPerGB = 1024 * 1024 * 1024

	// Default max file size (5MB).
	defaultMaxFileSize = 5 * bytesPerMB
)

// ErrFileTooLarge is returned when a file exceeds the maximum size limit.
var ErrFileTooLarge = errors.New("file exceeds maximum size limit")

// getMaxFileSize returns the maximum file size for downloads.
func getMaxFileSize() int64 {
	return GetConfig().MaxFileSize
}

// formatBytes formats bytes in a human-readable format.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// extractFileIDFromURL extracts the file ID from an S3 URL.
// Example:
// https://prod-files-secure.s3.us-west-2.amazonaws.com/workspace-id/7d399803-3851-448f-ac8e-c40d666389ee/Untitled.png
// Returns: 7d399803-3851-448f-ac8e-c40d666389ee.
func extractFileIDFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Check if it's an S3 URL
	if !strings.Contains(parsed.Host, "s3") || !strings.Contains(parsed.Host, "amazonaws.com") {
		return ""
	}

	// Path format: /workspace-id/file-id/filename
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= minFileURLSegments {
		// The file ID is the second part (index 1)
		return strings.ReplaceAll(parts[1], "-", "")
	}

	return ""
}

// downloadFile downloads a file from URL and saves it locally using streaming.
// This avoids loading the entire file into memory.
// Respects NTN_MAX_FILE_SIZE environment variable (default 5MB).
func (c *Crawler) downloadFile(ctx context.Context, fileURL, localPath string) error {
	maxSize := getMaxFileSize()
	c.logger.DebugContext(ctx, "downloading file", "url", fileURL, "path", localPath, "max_size", formatBytes(maxSize))

	// First, do a HEAD request to check size before downloading
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, fileURL, nil)
	if err != nil {
		return fmt.Errorf("create HEAD request: %w", err)
	}

	headResp, err := http.DefaultClient.Do(headReq)
	if err == nil {
		defer func() {
			_ = headResp.Body.Close()
		}()

		if headResp.ContentLength > maxSize {
			c.logger.WarnContext(ctx, "file exceeds size limit, skipping",
				"url", fileURL,
				"size", formatBytes(headResp.ContentLength),
				"limit", formatBytes(maxSize),
			)
			return ErrFileTooLarge
		}
	}
	// If HEAD fails, proceed with GET and check during download

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download file: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.WarnContext(ctx, "failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return apperrors.NewHTTPError(resp.StatusCode, "download failed")
	}

	// Check Content-Length from GET response as well
	if resp.ContentLength > maxSize {
		c.logger.WarnContext(ctx, "file exceeds size limit, skipping",
			"url", fileURL,
			"size", formatBytes(resp.ContentLength),
			"limit", formatBytes(maxSize),
		)
		return ErrFileTooLarge
	}

	// Use LimitReader as a safety net (server might send more than advertised)
	// Stream directly to file instead of loading into memory
	limitedReader := io.LimitReader(resp.Body, maxSize+1)

	written, err := c.tx.WriteStream(ctx, localPath, limitedReader)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Check if we hit the limit (file was larger than max size)
	if written > maxSize {
		c.logger.WarnContext(ctx, "file exceeds size limit during download, removing",
			"url", fileURL,
			"size_read", formatBytes(written),
			"limit", formatBytes(maxSize),
		)
		// Clean up the oversized file
		if delErr := c.tx.Delete(ctx, localPath); delErr != nil {
			c.logger.WarnContext(ctx, "failed to delete oversized file", "path", localPath, "error", delErr)
		}
		return ErrFileTooLarge
	}

	c.logger.InfoContext(ctx, "downloaded file", "path", localPath, "size", formatBytes(written))
	return nil
}

// loadFileManifest reads a .meta.json file and returns the FileManifest.
func (c *Crawler) loadFileManifest(ctx context.Context, metaPath string) (*FileManifest, error) {
	data, err := c.store.Read(ctx, metaPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest FileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}

	return &manifest, nil
}

// resolveFileConflict checks if a file with the same name already exists.
// If it does, and it's a different file (different file ID), appends a suffix.
// Returns the resolved filename and whether the file already exists (same ID).
func (c *Crawler) resolveFileConflict(ctx context.Context, filesDir, filename, fileID string) (string, bool) {
	ext := filepath.Ext(filename)
	baseName := strings.TrimSuffix(filename, ext)

	candidate := filename
	for range 10 { // Max 10 attempts to find unique name
		fullPath := filepath.Join(filesDir, candidate)
		metaPath := fullPath + ".meta.json"

		// Check if file exists
		if _, err := c.store.Read(ctx, fullPath); err != nil {
			// File doesn't exist, use this name
			return candidate, false
		}

		// File exists, check its manifest
		if manifest, err := c.loadFileManifest(ctx, metaPath); err == nil {
			if manifest.FileID == fileID {
				// Same file already downloaded
				return candidate, true
			}
		}
		// Different file or no manifest - add suffix and try again
		shortID := fileID
		if len(shortID) > shortIDLength {
			shortID = shortID[:shortIDLength]
		}
		candidate = fmt.Sprintf("%s-%s%s", baseName, shortID, ext)
	}

	return candidate, false
}

// processFileURL checks if a file needs to be downloaded and returns the local path.
// If the file is already downloaded, returns the existing local path.
// If the file is new, downloads it and returns the new local path.
// pageFilePath is the full path to the page's markdown file (e.g., "dir/page.md").
// pageID is the ID of the page/database containing this file.
// Files are saved in a "files" subdirectory under the page name (e.g., "dir/page/files/image.png").
//
//nolint:unparam // error return kept for API consistency
func (c *Crawler) processFileURL(ctx context.Context, fileURL, pageFilePath, pageID string) (string, error) {
	fileID := extractFileIDFromURL(fileURL)
	if fileID == "" {
		// Not an S3 URL, return original URL
		return fileURL, nil
	}

	// Check if file is already registered
	if reg, err := c.loadFileRegistry(ctx, fileID); err == nil {
		// File already downloaded, return local path
		return reg.FilePath, nil
	}

	// Extract filename from URL
	parsed, _ := url.Parse(fileURL)
	pathParts := strings.Split(parsed.Path, "/")
	filename := "file"
	if len(pathParts) > 0 {
		filename = pathParts[len(pathParts)-1]
		// URL decode the filename
		if decoded, err := url.QueryUnescape(filename); err == nil {
			filename = decoded
		}
	}

	// Sanitize filename but keep extension
	ext := filepath.Ext(filename)
	baseName := strings.TrimSuffix(filename, ext)
	sanitized := converter.SanitizeFilename(baseName)
	if sanitized == "" {
		sanitized = "file"
	}
	localFilename := sanitized + strings.ToLower(ext)

	// Build local path: dir/page/files/filename
	// From page path like "dir/page.md", create "dir/page/files/filename"
	pageDir := filepath.Dir(pageFilePath)
	pageBase := strings.TrimSuffix(filepath.Base(pageFilePath), ".md")
	filesDir := filepath.Join(pageDir, pageBase, "files")

	// Check for naming conflicts with existing files
	resolvedFilename, alreadyExists := c.resolveFileConflict(ctx, filesDir, localFilename, fileID)
	if alreadyExists {
		// Same file already downloaded (detected via manifest)
		localPath := filepath.Join(filesDir, resolvedFilename)
		c.logger.DebugContext(ctx, "file already exists with same ID", "path", localPath, "file_id", fileID)
		return localPath, nil
	}
	localPath := filepath.Join(filesDir, resolvedFilename)

	// Download the file
	if err := c.downloadFile(ctx, fileURL, localPath); err != nil {
		c.logger.WarnContext(ctx, "failed to download file", "url", fileURL, "error", err)
		return fileURL, nil // Return original URL on failure
	}

	// Save file registry
	reg := &FileRegistry{
		NtnsyncVersion: version.Version,
		ID:             fileID,
		FilePath:       localPath,
		SourceURL:      fileURL,
		LastSynced:     time.Now(),
	}
	if err := c.saveFileRegistry(ctx, reg); err != nil {
		c.logger.WarnContext(ctx, "failed to save file registry", "error", err)
	}

	// Write manifest file
	manifest := &FileManifest{
		NtnsyncVersion: version.Version,
		FileID:         fileID,
		ParentPageID:   pageID,
		DownloadedAt:   time.Now(),
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		manifestPath := localPath + ".meta.json"
		if err := c.tx.Write(ctx, manifestPath, manifestData); err != nil {
			c.logger.WarnContext(ctx, "failed to write file manifest", "error", err)
		}
	}

	return localPath, nil
}

// makeFileProcessor creates a converter.FileProcessor callback for converting file URLs.
// pageFilePath is the full path to the page's markdown file.
// pageID is the ID of the page/database containing files.
// Files are saved in a "files" subdirectory under the page name.
func (c *Crawler) makeFileProcessor(ctx context.Context, pageFilePath, pageID string) converter.FileProcessor {
	return func(fileURL string) string {
		localPath, err := c.processFileURL(ctx, fileURL, pageFilePath, pageID)
		if err != nil {
			return fileURL // Return original URL on error
		}
		// Convert absolute path to relative path from the page's directory
		pageDir := filepath.Dir(pageFilePath)
		relPath, err := filepath.Rel(pageDir, localPath)
		if err != nil {
			return localPath
		}
		return relPath
	}
}
