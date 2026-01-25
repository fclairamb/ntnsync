# Multi-Source Database Support

**Date:** 2026-01-15
**Status:** Proposal

## Problem

Notion API 2025-09-03 introduces multi-source databases where a single database can contain multiple linked data sources. The current implementation assumes a one-to-one relationship between databases and their content.

## Goal

Support multi-source databases while maintaining a clear file organization strategy that reflects the new data model.

## Multi-Source Database Concept

A Notion database can now function as a container for multiple data sources:

```
Projects Database (container)
├── Active Projects (data source 1)
├── Archived Projects (data source 2)
└── Template Projects (data source 3)
```

Each data source:
- Has its own set of pages
- Shares the same property schema (from the database container)
- Can have different filters/views
- Has a unique `data_source_id`

## File Organization Strategy

### Option 1: Flat Structure (Recommended)

Treat all pages from all data sources of a database as belonging to the same directory:

```
notion/
  teamspace-name/
    projects/                    # Database container directory
      project-alpha.md           # From data source 1
      project-beta.md            # From data source 1
      old-project.md             # From data source 2
      template-project.md        # From data source 3
```

**Pros:**
- Simple and intuitive
- Mirrors how users think about "all pages in a database"
- Minimal changes to existing structure

**Cons:**
- Cannot distinguish which data source a page belongs to without checking metadata

### Option 2: Data Source Subdirectories

Create subdirectories for each data source:

```
notion/
  teamspace-name/
    projects/                    # Database container directory
      active-projects/           # Data source 1
        project-alpha.md
        project-beta.md
      archived-projects/         # Data source 2
        old-project.md
      template-projects/         # Data source 3
        template-project.md
```

**Pros:**
- Clear separation of data sources
- Easy to see which data source a page belongs to

**Cons:**
- More complex directory structure
- Requires fetching data source names
- May not match user's mental model

### Recommendation

Use **Option 1 (Flat Structure)** because:
1. Most Notion databases will have a single data source (default case)
2. Users conceptually think of "pages in a database" not "pages in a data source"
3. Simpler implementation and migration path
4. Data source information is preserved in page frontmatter for tools that need it

## Page Metadata

Store data source information in frontmatter for all database pages:

```markdown
---
notion_id: abc123def456789
notion_parent_id: db-xyz789
notion_data_source_id: ds-xyz789data
notion_data_source_name: Active Projects
last_edited: 2026-01-15T10:30:00Z
notion_url: https://notion.so/abc123def456789
---

# Project Alpha

Content here...
```

## Registry Updates

### Page Registry: `.notion-sync/ids/page-${notion_id}.json`

Add data source tracking:

```json
{
  "id": "abc123def456",
  "space_id": "space123id",
  "file_path": "teamspace-name/projects/project-alpha.md",
  "title": "Project Alpha",
  "last_edited": "2026-01-15T10:30:00Z",
  "parent_type": "data_source",
  "parent_id": "ds-xyz789",
  "database_id": "db-xyz789",
  "data_source_id": "ds-xyz789data"
}
```

### New: Database Registry `.notion-sync/ids/database-${database_id}.json`

Track database containers and their data sources:

```json
{
  "id": "db-xyz789",
  "title": "Projects",
  "directory": "teamspace-name/projects",
  "data_sources": [
    {
      "id": "ds-xyz789-active",
      "name": "Active Projects",
      "is_default": true,
      "page_count": 15
    },
    {
      "id": "ds-xyz789-archived",
      "name": "Archived Projects",
      "is_default": false,
      "page_count": 42
    }
  ],
  "last_synced": "2026-01-15T10:30:00Z"
}
```

## Discovery and Caching

### Initial Database Discovery

When syncing a database for the first time:

1. Call `GET /v1/databases/{database_id}` to get container info
2. Extract all data sources from `database.data_sources[]`
3. Create database registry entry
4. For each data source, query pages with `POST /v1/data_sources/{data_source_id}/query`

### Data Source Caching

Implement a cache to avoid redundant API calls:

```go
type DataSourceCache struct {
    mu          sync.RWMutex
    databases   map[string]*DatabaseInfo
    dataSources map[string]*DataSourceInfo
    ttl         time.Duration
}

type DatabaseInfo struct {
    ID          string
    Title       string
    DataSources []DataSourceInfo
    CachedAt    time.Time
}

type DataSourceInfo struct {
    ID         string
    DatabaseID string
    Name       string
    IsDefault  bool
    CachedAt   time.Time
}
```

**Cache invalidation:**
- Refresh after 1 hour (default TTL)
- Force refresh on explicit sync command
- Invalidate when encountering API errors

## Backward Compatibility

### Handling Old Database References

When encountering a `database_id` from old data:

1. Resolve to `data_source_id` via cache or API call
2. Update registry with both IDs
3. Use `data_source_id` for all API operations
4. Maintain `database_id` for user-facing references

### Migration from Old Registry

```go
func migrateOldRegistry(oldPage PageRegistry) PageRegistry {
    if oldPage.ParentType == "database_id" {
        // Resolve database_id to data_source_id
        dataSourceID, err := resolveDataSourceID(ctx, oldPage.ParentID)
        if err != nil {
            log.Warn("Failed to resolve data source", "database_id", oldPage.ParentID)
            return oldPage
        }

        oldPage.DatabaseID = oldPage.ParentID
        oldPage.DataSourceID = dataSourceID
        oldPage.ParentType = "data_source"
        oldPage.ParentID = dataSourceID
    }
    return oldPage
}
```

## API Operations

### Querying Database Pages

```go
func (c *Client) QueryDataSource(ctx context.Context, dataSourceID string, filter *Filter) ([]*Page, error) {
    url := fmt.Sprintf("https://api.notion.com/v1/data_sources/%s/query", dataSourceID)

    req, err := http.NewRequestWithContext(ctx, "POST", url, bodyReader)
    if err != nil {
        return nil, err
    }

    req.Header.Set("Notion-Version", "2025-09-03")
    req.Header.Set("Authorization", "Bearer "+c.token)

    // ... execute request and parse response
}
```

### Creating Pages in Data Sources

```go
func (c *Client) CreatePage(ctx context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error) {
    body := map[string]interface{}{
        "parent": map[string]interface{}{
            "type":           "data_source_id",
            "data_source_id": dataSourceID,
        },
        "properties": properties,
    }

    // ... execute POST to /v1/pages
}
```

## Testing Scenarios

1. **Single data source database** (most common case)
   - Verify correct resolution
   - Ensure backward compatibility

2. **Multi-source database**
   - Pages from different sources in same directory
   - Correct metadata in frontmatter
   - Registry tracks all data sources

3. **Migration from old API**
   - Old registry files work correctly
   - IDs are resolved and updated

4. **Cache behavior**
   - Cache hits reduce API calls
   - Stale cache is refreshed
   - Cache invalidation works

## Design Decisions

- **Flat directory structure**: Simpler mental model, matches user expectations
- **Store both database_id and data_source_id**: Enables backward compatibility and future flexibility
- **Cache data source mappings**: Reduces API calls and improves performance
- **Frontmatter includes data source info**: Enables external tools to work with the data
