package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	// API pagination settings.
	defaultPageSize = 100 // Default number of results per page

	// Notion ID format constants.
	notionIDLength         = 32 // Length of a Notion ID without dashes
	notionIDWithDashLength = 36 // Length of a Notion ID with dashes (UUID format: 8-4-4-4-12)
	uuidSegmentCount       = 5  // Number of segments in a UUID
)

// GetPage retrieves a page by ID.
func (c *Client) GetPage(ctx context.Context, pageID string) (*Page, error) {
	c.logger.DebugContext(ctx, "Fetching page", slog.String("pageId", pageID))

	before := time.Now()

	var page Page
	path := "/pages/" + pageID
	if err := c.do(ctx, "GET", path, nil, &page); err != nil {
		return nil, fmt.Errorf("get page %s: %w", pageID, err)
	}
	c.logger.DebugContext(ctx, "Page fetched", "time_spent_ms", time.Since(before).Milliseconds())
	return &page, nil
}

// GetDatabaseContainer retrieves database container info with data sources list (API 2025-09-03+).
func (c *Client) GetDatabaseContainer(ctx context.Context, databaseID string) (*DatabaseContainer, error) {
	c.logger.DebugContext(ctx, "Fetching database container", slog.String("databaseId", databaseID))

	var container DatabaseContainer
	path := "/databases/" + databaseID
	if err := c.do(ctx, "GET", path, nil, &container); err != nil {
		return nil, fmt.Errorf("get database container %s: %w", databaseID, err)
	}
	return &container, nil
}

// GetDataSource retrieves a data source by ID (schema and properties).
func (c *Client) GetDataSource(ctx context.Context, dataSourceID string) (*DataSource, error) {
	c.logger.DebugContext(ctx, "Fetching data source", slog.String("dataSourceId", dataSourceID))

	var ds DataSource
	path := "/data_sources/" + dataSourceID
	if err := c.do(ctx, "GET", path, nil, &ds); err != nil {
		return nil, fmt.Errorf("get data source %s: %w", dataSourceID, err)
	}
	return &ds, nil
}

// GetDatabase retrieves a database by ID.
// In API 2025-09-03+, this fetches the container and first data source to build
// a backwards-compatible Database struct.
func (c *Client) GetDatabase(ctx context.Context, databaseID string) (*Database, error) {
	c.logger.DebugContext(ctx, "Fetching database", slog.String("databaseId", databaseID))

	before := time.Now()

	// Get container (returns list of data sources)
	container, err := c.GetDatabaseContainer(ctx, databaseID)
	if err != nil {
		return nil, err
	}

	if len(container.DataSources) == 0 {
		return nil, fmt.Errorf("database %s: %w", databaseID, apperrors.ErrNoDataSources)
	}

	// Get first data source for schema
	dataSource, err := c.GetDataSource(ctx, container.DataSources[0].ID)
	if err != nil {
		return nil, fmt.Errorf("get data source for database %s: %w", databaseID, err)
	}

	c.logger.DebugContext(ctx, "Database fetched", "time_spent_ms", time.Since(before).Milliseconds())

	// Return backwards-compatible Database struct
	return &Database{
		Object:         container.Object,
		ID:             container.ID,
		CreatedTime:    container.CreatedTime,
		LastEditedTime: container.LastEditedTime,
		CreatedBy:      container.CreatedBy,
		LastEditedBy:   container.LastEditedBy,
		Title:          container.Title,
		Description:    container.Description,
		Icon:           container.Icon,
		Cover:          container.Cover,
		Properties:     dataSource.Properties,
		Parent:         container.Parent,
		URL:            container.URL,
		PublicURL:      container.PublicURL,
		Archived:       container.Archived,
		InTrash:        container.InTrash,
		IsInline:       container.IsInline,
		DataSourceID:   dataSource.ID,
		DataSources:    container.DataSources,
	}, nil
}

// QueryDataSource queries a data source and returns all pages (API 2025-09-03+).
func (c *Client) QueryDataSource(ctx context.Context, dataSourceID string) ([]DatabasePage, error) {
	c.logger.DebugContext(ctx, "Querying data source", slog.String("dataSourceId", dataSourceID))

	var allPages []DatabasePage
	var cursor string

	for {
		body := map[string]any{
			"page_size": defaultPageSize,
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}

		var result QueryDatabaseResponse
		path := fmt.Sprintf("/data_sources/%s/query", dataSourceID)
		if err := c.do(ctx, "POST", path, body, &result); err != nil {
			return nil, fmt.Errorf("query data source %s: %w", dataSourceID, err)
		}

		allPages = append(allPages, result.Results...)

		if !result.HasMore || result.NextCursor == nil {
			break
		}
		cursor = *result.NextCursor
	}

	c.logger.InfoContext(ctx, "data source query complete",
		"data_source_id", dataSourceID,
		"pages_found", len(allPages))
	return allPages, nil
}

// QueryDatabase queries a database and returns all pages.
// In API 2025-09-03+, this resolves the database to its first data source
// and queries that data source.
func (c *Client) QueryDatabase(ctx context.Context, databaseID string) ([]DatabasePage, error) {
	c.logger.DebugContext(ctx, "Querying database", slog.String("databaseId", databaseID))

	// Resolve data source ID from database ID
	container, err := c.GetDatabaseContainer(ctx, databaseID)
	if err != nil {
		return nil, err
	}

	if len(container.DataSources) == 0 {
		return nil, fmt.Errorf("database %s: %w", databaseID, apperrors.ErrNoDataSources)
	}

	// Query first data source
	return c.QueryDataSource(ctx, container.DataSources[0].ID)
}

// SearchFilter configures the search query.
type SearchFilter struct {
	Query       string
	FilterType  string // "page" or "data_source" (use "database" for backwards compat, mapped to "data_source")
	StartCursor string
	PageSize    int
	// Sort by last_edited_time (only "ascending" or "descending" for timestamp sorting)
	SortDirection string // "ascending" or "descending"
}

// Search searches for pages and databases.
func (c *Client) Search(ctx context.Context, filter SearchFilter) (*SearchResponse, error) {
	body := map[string]any{}

	if filter.Query != "" {
		body["query"] = filter.Query
	}

	if filter.FilterType != "" {
		filterValue := filter.FilterType
		// API 2025-09-03 uses "data_source" instead of "database"
		if filterValue == "database" {
			filterValue = "data_source"
		}
		body["filter"] = map[string]string{
			"value":    filterValue,
			"property": "object",
		}
	}

	if filter.StartCursor != "" {
		body["start_cursor"] = filter.StartCursor
	}

	if filter.PageSize > 0 {
		body["page_size"] = filter.PageSize
	} else {
		body["page_size"] = 100
	}

	// Add sort if specified.
	// Notion Search API only supports sorting, not filtering by timestamp.
	if filter.SortDirection != "" {
		body["sort"] = map[string]string{
			"direction": filter.SortDirection,
			"timestamp": "last_edited_time",
		}
	}

	searchQuery, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("couldn't do serialization: %w", err)
	}

	var result SearchResponse
	c.logger.DebugContext(ctx, "Performing search", slog.String("query", string(searchQuery)))
	timeBefore := time.Now()
	if err := c.do(ctx, "POST", "/search", body, &result); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	c.logger.DebugContext(ctx, "Search returned", "time_spent_ms", time.Since(timeBefore).Milliseconds())

	return &result, nil
}

// SearchAllPages retrieves all pages accessible to the integration.
// The Notion Search API does not support timestamp filtering.
// All pages are fetched and sorted by last_edited_time (descending = newest first).
// Callers should filter results by timestamp after retrieval.
func (c *Client) SearchAllPages(ctx context.Context) ([]Page, error) {
	return c.SearchAllPagesWithStop(ctx, nil)
}

// SearchAllPagesWithStop retrieves all pages accessible to the integration with optional early stopping.
// The shouldStop function is called after each page batch. If it returns true, pagination stops.
// Pages are sorted by last_edited_time (descending = newest first).
func (c *Client) SearchAllPagesWithStop(ctx context.Context, shouldStop func([]Page) bool) ([]Page, error) {
	var allPages []Page
	var cursor string

	for {
		result, err := c.Search(ctx, SearchFilter{
			FilterType:    "page",
			StartCursor:   cursor,
			PageSize:      defaultPageSize,
			SortDirection: "descending", // Newest pages first
		})
		if err != nil {
			return nil, err
		}

		allPages = append(allPages, result.Results...)

		// Check if caller wants to stop early
		if shouldStop != nil && shouldStop(allPages) {
			c.logger.InfoContext(ctx, "search stopped early by caller", "pages_fetched", len(allPages))
			return allPages, nil
		}

		if !result.HasMore || result.NextCursor == nil {
			break
		}
		cursor = *result.NextCursor
	}

	c.logger.InfoContext(ctx, "discovered pages", "count", len(allPages))
	return allPages, nil
}

// SearchWorkspacePages retrieves all pages at workspace level (root pages).
// These are pages whose parent is a workspace or teamspace, not another page.
// It searches incrementally and logs progress.
func (c *Client) SearchWorkspacePages(ctx context.Context) ([]Page, error) {
	var workspacePages []Page
	var cursor string
	totalSearched := 0
	batchNum := 0

	for {
		batchNum++
		result, err := c.Search(ctx, SearchFilter{
			FilterType:  "page",
			StartCursor: cursor,
			PageSize:    defaultPageSize,
		})
		if err != nil {
			return nil, err
		}

		totalSearched += len(result.Results)

		// Filter workspace-level pages from this batch
		batchWorkspacePages := 0
		for i := range result.Results {
			page := &result.Results[i]
			if page.Parent.IsWorkspaceLevel() {
				workspacePages = append(workspacePages, *page)
				batchWorkspacePages++
				c.logger.InfoContext(ctx, "found root page",
					"title", page.Title(),
					"space_id", page.Parent.SpaceID)
			}
		}

		c.logger.InfoContext(ctx, "search progress",
			"batch", batchNum,
			"pages_searched", totalSearched,
			"root_pages_found", len(workspacePages))

		if !result.HasMore || result.NextCursor == nil {
			break
		}
		cursor = *result.NextCursor
	}

	c.logger.InfoContext(ctx, "search complete",
		"total_pages_searched", totalSearched,
		"root_pages_found", len(workspacePages))
	return workspacePages, nil
}

// Bot represents the response from /users/me for a bot.
type Bot struct {
	Object string `json:"object"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Bot    struct {
		Owner struct {
			Type      string `json:"type"`
			Workspace bool   `json:"workspace"`
		} `json:"owner"`
		WorkspaceName string `json:"workspace_name"`
	} `json:"bot"`
}

// GetMe retrieves information about the current bot/user.
func (c *Client) GetMe(ctx context.Context) (*Bot, error) {
	var bot Bot
	if err := c.do(ctx, "GET", "/users/me", nil, &bot); err != nil {
		return nil, fmt.Errorf("get me: %w", err)
	}
	return &bot, nil
}

// NormalizeID removes dashes from a Notion ID.
func NormalizeID(id string) string {
	return url.PathEscape(id)
}

// ParsePageIDOrURL extracts a Notion page ID from a URL or returns the ID if already bare.
// Handles various formats:
// - https://www.notion.so/Page-Title-abc123def456
// - https://notion.so/workspace/Page-abc123def456
// - abc123def456 (raw ID without dashes)
// - abc123-def4-5678-90ab-cdef12345678 (raw ID with dashes).
func ParsePageIDOrURL(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", apperrors.ErrEmptyInput
	}

	// Check if it's a URL
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return extractPageIDFromURL(input)
	}

	// Not a URL - treat as raw ID
	// Remove dashes and validate
	cleanID := strings.ReplaceAll(input, "-", "")

	// Notion IDs are 32 hex characters
	if len(cleanID) != notionIDLength {
		return "", fmt.Errorf("%w (expected 32 chars, got %d): %s", apperrors.ErrInvalidPageIDFormat, len(cleanID), cleanID)
	}

	if !isHexString(cleanID) {
		return "", fmt.Errorf("%w (not hexadecimal): %s", apperrors.ErrInvalidPageIDFormat, cleanID)
	}

	return cleanID, nil
}

// extractPageIDFromURL extracts a Notion page ID from a URL.
func extractPageIDFromURL(input string) (string, error) {
	parsedURL, err := url.Parse(input)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	// Notion URLs have the page ID at the end of the path or as a query parameter
	// Format: /workspace/Page-Title-{pageID} or /{pageID}
	path := strings.Trim(parsedURL.Path, "/")

	// The page ID is typically the last segment after the last hyphen
	// or the entire path if it looks like an ID
	parts := strings.Split(path, "/")
	lastPart := parts[len(parts)-1]

	// Look for the ID in the last part (after last hyphen, at least 32 chars)
	// Notion IDs are 32 characters (hex)
	if len(lastPart) >= notionIDLength {
		// Find the last 32-character segment that looks like a hex ID
		idStart := len(lastPart) - notionIDLength
		possibleID := lastPart[idStart:]

		// Check if it's a valid hex string
		if isHexString(possibleID) {
			return strings.ReplaceAll(possibleID, "-", ""), nil
		}
	}

	// Try to find ID with dashes (36 chars: 8-4-4-4-12)
	if strings.Contains(lastPart, "-") {
		segments := strings.Split(lastPart, "-")
		// Look for UUID-like pattern at the end
		if len(segments) >= uuidSegmentCount {
			// Take last 5 segments (UUID format)
			uuidParts := segments[len(segments)-uuidSegmentCount:]
			possibleUUID := strings.Join(uuidParts, "-")
			if len(possibleUUID) == notionIDWithDashLength {
				return strings.ReplaceAll(possibleUUID, "-", ""), nil
			}
		}
	}

	return "", fmt.Errorf("%w: %s", apperrors.ErrInvalidPageIDFormat, input)
}

// isHexString checks if a string contains only hexadecimal characters.
func isHexString(str string) bool {
	for _, c := range str {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(str) > 0
}
