// Package sync provides synchronization logic between Notion and local storage.
package sync

import (
	"slices"
	"time"

	"github.com/fclairamb/ntnsync/internal/version"
)

const (
	// stateFormatVersion is the current version of the state file format.
	// Increment this when making breaking changes to the state structure.
	stateFormatVersion = 3
)

// State is persisted in .notion-sync/state.json
// Simplified to only contain folder names. Page metadata is stored in:
// - Frontmatter of markdown files (last_synced, file_path)
// - Page registries (.notion-sync/ids/page-{id}.json).
type State struct {
	NtnsyncVersion   string     `json:"ntnsync_version"`
	Version          int        `json:"version"`
	Folders          []string   `json:"folders"`
	LastPullTime     *time.Time `json:"last_pull_time,omitempty"`
	OldestPullResult *time.Time `json:"oldest_pull_result,omitempty"` // Oldest page seen in last pull
}

// NewState creates a new empty state.
func NewState() *State {
	return &State{
		NtnsyncVersion: version.Version,
		Version:        stateFormatVersion,
		Folders:        []string{},
	}
}

// HasFolder checks if a folder exists in state.
func (s *State) HasFolder(folder string) bool {
	return slices.Contains(s.Folders, folder)
}

// AddFolder adds a folder to state if not already present.
func (s *State) AddFolder(folder string) {
	if !s.HasFolder(folder) {
		s.Folders = append(s.Folders, folder)
	}
}

// PageRegistry is stored in .notion-sync/ids/page-{id}.json
// Contains all metadata needed to locate and identify a page or database.
type PageRegistry struct {
	NtnsyncVersion string    `json:"ntnsync_version"`
	ID             string    `json:"id"`
	Type           string    `json:"type"` // "page" or "database"
	Folder         string    `json:"folder"`
	FilePath       string    `json:"file_path"`
	Title          string    `json:"title"`
	LastEdited     time.Time `json:"last_edited"`
	LastSynced     time.Time `json:"last_synced"`
	IsRoot         bool      `json:"is_root"`
	ParentID       string    `json:"parent_id,omitempty"`
	Children       []string  `json:"children,omitempty"`
	ContentHash    string    `json:"content_hash,omitempty"`
}

// FileRegistry is stored in .notion-sync/ids/file-{id}.json
// Contains metadata for tracking downloaded files (images, attachments, etc.).
type FileRegistry struct {
	NtnsyncVersion string    `json:"ntnsync_version"`
	ID             string    `json:"id"`         // File ID extracted from S3 URL
	FilePath       string    `json:"file_path"`  // Local file path (directory + name)
	SourceURL      string    `json:"source_url"` // Original S3 URL
	LastSynced     time.Time `json:"last_synced"`
}

// FileManifest is stored alongside downloaded files as {filename}.meta.json
// Contains metadata for local file identification.
type FileManifest struct {
	NtnsyncVersion string    `json:"ntnsync_version"`
	FileID         string    `json:"file_id"`
	ParentPageID   string    `json:"parent_page_id"`
	DownloadedAt   time.Time `json:"downloaded_at"`
}
