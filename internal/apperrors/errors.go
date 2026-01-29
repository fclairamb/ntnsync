// Package apperrors provides common static errors used throughout the application.
package apperrors

import (
	"errors"
	"fmt"
)

// HTTPError represents an HTTP error with a status code.
type HTTPError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// NewHTTPError creates a new HTTPError.
func NewHTTPError(statusCode int, body string) *HTTPError {
	return &HTTPError{StatusCode: statusCode, Body: body}
}

// Common static errors used throughout the application.
var (
	// ErrPageIDRequired is returned when a page ID or URL is required but not provided.
	ErrPageIDRequired = errors.New("page ID or URL required")

	// ErrNotLocalStore is returned when an operation requires a LocalStore but a different store type was provided.
	ErrNotLocalStore = errors.New("store is not a LocalStore")

	// ErrRemoteNotConfigured is returned when a git remote operation is attempted but no remote is configured.
	ErrRemoteNotConfigured = errors.New("no remote configured")

	// ErrRemoteNotConfiguredSetURL is returned when push/pull is attempted without NTN_GIT_URL set.
	ErrRemoteNotConfiguredSetURL = errors.New("remote not configured (set NTN_GIT_URL)")

	// ErrNotionTokenRequired is returned when a Notion token is required but not provided.
	ErrNotionTokenRequired = errors.New("notion token required (--token or NOTION_TOKEN env var)")

	// ErrHTTPSPasswordRequired is returned when HTTPS git URL is used without NTN_GIT_PASS.
	ErrHTTPSPasswordRequired = errors.New("NTN_GIT_PASS required for HTTPS URLs")

	// ErrMaxRetriesExceeded is returned when the maximum number of retries is exceeded.
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")

	// ErrEmptyInput is returned when an empty input is provided.
	ErrEmptyInput = errors.New("empty input")

	// ErrInvalidPageIDFormat is returned when a page ID has an invalid format.
	ErrInvalidPageIDFormat = errors.New("invalid page ID format")

	// ErrUnexpectedBlockParentType is returned when a block has an unexpected parent type.
	ErrUnexpectedBlockParentType = errors.New("unexpected block parent type")

	// ErrMaxDepthExceeded is returned when the maximum depth is exceeded while resolving block parent chain.
	ErrMaxDepthExceeded = errors.New("max depth exceeded while resolving block parent chain")

	// ErrNoFrontmatter is returned when no frontmatter is found in a markdown file.
	ErrNoFrontmatter = errors.New("no frontmatter found")

	// ErrFrontmatterNotClosed is returned when frontmatter is not properly closed.
	ErrFrontmatterNotClosed = errors.New("frontmatter not closed")

	// ErrNoPreviousPullTime is returned when no previous pull time is found and --since flag is not specified.
	ErrNoPreviousPullTime = errors.New("no previous pull time found, please use --since flag to specify duration")

	// ErrFolderNameEmpty is returned when a folder name is empty.
	ErrFolderNameEmpty = errors.New("folder name cannot be empty")

	// ErrFolderNameInvalid is returned when a folder name contains invalid characters.
	ErrFolderNameInvalid = errors.New("folder name must contain only lowercase letters, numbers, and hyphens")

	// ErrTransactionCommitted is returned when attempting to use a transaction that has already been committed.
	ErrTransactionCommitted = errors.New("transaction already committed")

	// ErrCycleDetected is returned when a cycle is detected in page hierarchy.
	ErrCycleDetected = errors.New("cycle detected in page hierarchy")

	// ErrInvalidRootMdTable is returned when root.md has invalid table format.
	ErrInvalidRootMdTable = errors.New("expected table separator line after header")

	// ErrInvalidRootMdRow is returned when a row in root.md has invalid format.
	ErrInvalidRootMdRow = errors.New("invalid row format")

	// ErrNoDataSources is returned when a database has no data sources.
	ErrNoDataSources = errors.New("database has no data sources")
)
