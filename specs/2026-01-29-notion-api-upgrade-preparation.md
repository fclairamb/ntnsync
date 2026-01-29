# Prepare for Notion API 2025-09-03 Upgrade

**Date:** 2026-01-29
**Status:** Proposal

## Problem

The application currently uses Notion API version `2022-06-28` (defined in `internal/notion/client.go:45`). The latest API version is `2025-09-03`, which introduces **breaking changes** for multi-source databases. If a user adds another data source to a database, the following operations will fail with older API versions:

- Creating pages with the database as parent
- Database read, write, or query operations
- Writing relation properties pointing to that database

## Goal

Upgrade to Notion API version `2025-09-03` to:
- Prevent disruption when users enable multi-source databases
- Use the new `/v1/data_sources` endpoints for database operations
- Maintain compatibility with future Notion API changes

## Current Implementation Analysis

### Files Requiring Changes

| File | Current Usage | Required Changes |
|------|---------------|------------------|
| `internal/notion/client.go:45` | `APIVersion = "2022-06-28"` | Update to `"2025-09-03"` |
| `internal/notion/types.go:146-152` | `Parent.DatabaseID` field | Add `DataSourceID` field |
| `internal/notion/types.go:28-47` | `Database` struct | Add `DataSources` field for container response |
| `internal/notion/pages.go:41-53` | `GetDatabase()` at `/databases/{id}` | Now returns data sources list, not schema |
| `internal/notion/pages.go:56-88` | `QueryDatabase()` at `/databases/{id}/query` | Move to `/data_sources/{id}/query` |
| `internal/notion/pages.go:100-148` | `Search()` with `filter.FilterType = "database"` | Change to `"data_source"` |

## Implementation Plan

### Phase 1: Add New Types

Add new types to support the 2025-09-03 API:

```go
// DataSource represents a data source within a database container.
type DataSource struct {
    Object         string         `json:"object"` // "data_source"
    ID             string         `json:"id"`
    Name           string         `json:"name"`
    CreatedTime    time.Time      `json:"created_time"`
    LastEditedTime time.Time      `json:"last_edited_time"`
    CreatedBy      User           `json:"created_by"`
    LastEditedBy   User           `json:"last_edited_by"`
    Title          []RichText     `json:"title"`
    Description    []RichText     `json:"description"`
    Properties     map[string]any `json:"properties"` // Schema
    Parent         Parent         `json:"parent"`
    URL            string         `json:"url"`
    Archived       bool           `json:"archived"`
    InTrash        bool           `json:"in_trash"`
}

// DatabaseContainer represents the new database container response.
// In 2025-09-03, GET /databases/{id} returns this instead of schema.
type DatabaseContainer struct {
    Object      string       `json:"object"` // "database"
    ID          string       `json:"id"`
    DataSources []DataSource `json:"data_sources"`
    Title       []RichText   `json:"title"`
    Icon        *Icon        `json:"icon"`
    Cover       *FileBlock   `json:"cover"`
    Parent      Parent       `json:"parent"`
    URL         string       `json:"url"`
    IsInline    bool         `json:"is_inline"`
    Archived    bool         `json:"archived"`
    InTrash     bool         `json:"in_trash"`
}
```

Update `Parent` struct:

```go
type Parent struct {
    Type         string `json:"type"`
    PageID       string `json:"page_id,omitempty"`
    DatabaseID   string `json:"database_id,omitempty"`   // Keep for backwards compat
    DataSourceID string `json:"data_source_id,omitempty"` // New in 2025-09-03
    BlockID      string `json:"block_id,omitempty"`
    Workspace    bool   `json:"workspace,omitempty"`
    SpaceID      string `json:"space_id,omitempty"`
}
```

### Phase 2: Add New API Methods

Add new methods for data source operations:

```go
// GetDataSource retrieves a data source by ID (schema and properties).
func (c *Client) GetDataSource(ctx context.Context, dataSourceID string) (*DataSource, error) {
    var ds DataSource
    path := "/data_sources/" + dataSourceID
    if err := c.do(ctx, "GET", path, nil, &ds); err != nil {
        return nil, fmt.Errorf("get data source %s: %w", dataSourceID, err)
    }
    return &ds, nil
}

// QueryDataSource queries a data source and returns all pages.
func (c *Client) QueryDataSource(ctx context.Context, dataSourceID string) ([]DatabasePage, error) {
    var allPages []DatabasePage
    var cursor string

    for {
        body := map[string]any{"page_size": defaultPageSize}
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

    return allPages, nil
}

// GetDatabaseContainer retrieves database container info with data sources list.
func (c *Client) GetDatabaseContainer(ctx context.Context, databaseID string) (*DatabaseContainer, error) {
    var db DatabaseContainer
    path := "/databases/" + databaseID
    if err := c.do(ctx, "GET", path, nil, &db); err != nil {
        return nil, fmt.Errorf("get database container %s: %w", databaseID, err)
    }
    return &db, nil
}
```

### Phase 3: Update Existing Methods

Modify `GetDatabase()` to:
1. Call `GET /databases/{id}` to get the container
2. Extract the first data source ID
3. Call `GET /data_sources/{id}` to get the schema
4. Return a unified `Database` struct for backwards compatibility

```go
func (c *Client) GetDatabase(ctx context.Context, databaseID string) (*Database, error) {
    // Get container (returns list of data sources)
    container, err := c.GetDatabaseContainer(ctx, databaseID)
    if err != nil {
        return nil, err
    }

    if len(container.DataSources) == 0 {
        return nil, fmt.Errorf("database %s has no data sources", databaseID)
    }

    // Get first data source for schema (most common case)
    ds := &container.DataSources[0]

    // Return backwards-compatible Database struct
    return &Database{
        Object:         container.Object,
        ID:             container.ID,
        Title:          container.Title,
        Icon:           container.Icon,
        Cover:          container.Cover,
        Properties:     ds.Properties,
        Parent:         container.Parent,
        URL:            container.URL,
        IsInline:       container.IsInline,
        Archived:       container.Archived,
        InTrash:        container.InTrash,
        // New fields
        DataSourceID:   ds.ID,
        DataSources:    container.DataSources,
    }, nil
}
```

Modify `QueryDatabase()` to use data source ID:

```go
func (c *Client) QueryDatabase(ctx context.Context, databaseID string) ([]DatabasePage, error) {
    // Resolve data source ID from database ID
    container, err := c.GetDatabaseContainer(ctx, databaseID)
    if err != nil {
        return nil, err
    }

    if len(container.DataSources) == 0 {
        return nil, fmt.Errorf("database %s has no data sources", databaseID)
    }

    // Query first data source
    return c.QueryDataSource(ctx, container.DataSources[0].ID)
}
```

### Phase 4: Update Search Filter

Update search to use new filter value:

```go
// In Search() method
if filter.FilterType == "database" {
    // API 2025-09-03 uses "data_source" instead of "database"
    body["filter"] = map[string]string{
        "value":    "data_source",
        "property": "object",
    }
}
```

### Phase 5: Update Registry and Frontmatter

Add `data_source_id` to registry entries:

```go
type RegistryEntry struct {
    ID             string    `json:"id"`
    ParentID       string    `json:"parent_id"`
    DataSourceID   string    `json:"data_source_id,omitempty"` // New
    FilePath       string    `json:"file_path"`
    LastEdited     time.Time `json:"last_edited"`
    NotionType     string    `json:"notion_type"`
}
```

Add to frontmatter for database pages:

```markdown
---
notion_id: abc123
notion_parent_id: xyz789
notion_data_source_id: ds_xyz789
last_edited: 2026-01-29T10:30:00Z
---
```

### Phase 6: Update API Version Header

```go
const (
    APIVersion = "2025-09-03"
)
```

## Migration Considerations

### Backwards Compatibility

- Keep `DatabaseID` field in `Parent` struct (API returns both in responses)
- Keep `GetDatabase()` returning `*Database` with same signature
- Keep `QueryDatabase()` working with database IDs (resolve to data source internally)
- Registry entries without `data_source_id` will be populated on first sync

### Caching

Add data source ID resolution caching to avoid repeated container lookups:

```go
type dataSourceCache struct {
    mu    sync.RWMutex
    cache map[string]string // databaseID -> dataSourceID
}

func (c *Client) resolveDataSourceID(ctx context.Context, databaseID string) (string, error) {
    // Check cache first
    c.dsCache.mu.RLock()
    if dsID, ok := c.dsCache.cache[databaseID]; ok {
        c.dsCache.mu.RUnlock()
        return dsID, nil
    }
    c.dsCache.mu.RUnlock()

    // Fetch and cache
    container, err := c.GetDatabaseContainer(ctx, databaseID)
    if err != nil {
        return "", err
    }

    if len(container.DataSources) == 0 {
        return "", fmt.Errorf("no data sources")
    }

    dsID := container.DataSources[0].ID

    c.dsCache.mu.Lock()
    c.dsCache.cache[databaseID] = dsID
    c.dsCache.mu.Unlock()

    return dsID, nil
}
```

## Testing Strategy

1. **Unit tests**: Mock API responses with new data source format
2. **Integration tests**: Test against real Notion workspace with single and multi-source databases
3. **Backwards compatibility**: Verify existing registry files still work
4. **Migration test**: Ensure smooth upgrade from current state

## Rollout

This upgrade will be implemented as a single big-bang change in one PR:

1. Implement all type changes (`DataSource`, `DatabaseContainer`, updated `Parent`)
2. Add new API methods (`GetDataSource`, `QueryDataSource`, `GetDatabaseContainer`)
3. Update existing methods to use data source endpoints internally
4. Update search filter value
5. Update API version constant to `"2025-09-03"`
6. Update registry and frontmatter support
7. Run full test suite
8. Release as a new minor version (e.g., v0.5.0)

**Rationale for big-bang approach:**
- The changes are interdependent (API version affects all endpoints)
- Partial migration is not possible (old endpoints fail with new version header)
- The codebase is small enough to update in one pass
- Existing registry files remain compatible (new fields are optional)

## References

- [Changes by version](https://developers.notion.com/reference/changes-by-version)
- [Upgrading to Version 2025-09-03](https://developers.notion.com/docs/upgrade-guide-2025-09-03)
- Existing spec: `specs/2026-01-15-notion-api-2025-09-03-upgrade.md`
