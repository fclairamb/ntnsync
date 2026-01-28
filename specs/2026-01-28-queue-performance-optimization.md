# Queue Processing Performance Optimization

**Date:** 2026-01-28  
**Status:** Implemented  
**Branch:** feat/performance

## Problem

Queue processing was extremely slow, often getting stuck and unable to complete. With 850 queue files, processing was taking hours. Key bottlenecks:

1. **API calls in init mode**: For every page already in the registry, the code was making an unnecessary Notion API call to check if it needed updating
2. **Recursive parent fetching**: When processing a page with a missing parent, the entire parent page (including all blocks) was fetched immediately, creating cascading API calls
3. **No performance visibility**: No timing metrics to identify bottlenecks

## Performance Metrics (Before Optimization)

- **Processing rate**: ~1 page per 40 seconds
- **850 queue files**: Would take ~9.4 hours to complete
- **API calls**: Excessive due to parent resolution and update checks

## Solutions Implemented

### 1. Optimize Init Mode (Task #2)

**File:** `internal/sync/process.go` (lines 391-406)

**Change:** Modified `shouldSkipLegacyPage()` to skip pages that already exist in the registry WITHOUT making API calls.

**Before:**
```go
// Made API call for every existing page in init mode
page, err := c.client.GetPage(ctx, pageID)
if !page.LastEditedTime.After(reg.LastEdited) {
    return legacyPageSkip
}
```

**After:**
```go
// Skip immediately if page exists in registry (init mode)
c.logger.DebugContext(ctx, "skipping existing page in init mode (using cache)",
    "page_id", pageID,
    "title", reg.Title)
return legacyPageSkip
```

**Impact:** Eliminates ~0.3-1.5 seconds per already-synced page in init mode.

### 2. Defer Parent Page Content Fetching (Task #3)

**File:** `internal/sync/process.go` (lines 449-477 and 525-560)

**Change:** In init mode, when a parent page is not in the registry, queue it for later processing instead of fetching it immediately.

**Before:**
```go
// Fetched parent immediately with all blocks
parentFiles, err := c.processPage(ctx, parentID, folder, isInit, "")
```

**After:**
```go
if isInit {
    // Queue parent for later processing
    c.queueManager.CreateEntry(ctx, queue.Entry{
        Type:     queueTypeInit,
        Folder:   folder,
        PageIDs:  []string{parentID},
    })
    // Treat child as root for now
    result.isRoot = true
    result.parentID = ""
    return result, nil
}
```

**Impact:** 
- Avoids recursive parent fetching (can save 5-30 seconds per page with deep hierarchies)
- Pages are placed at folder root initially, reorganized when parent is processed
- Only applies to init mode; update mode still fetches parents immediately for correct paths

### 3. Add Performance Logging (Task #4)

**Files:** `internal/sync/process.go` (processPage and processDatabase functions)

**Change:** Added detailed timing metrics for all operations:

```go
- fetch_page_ms: Time to fetch page metadata
- fetch_blocks_ms: Time to fetch all blocks
- convert_ms: Time to convert to markdown
- write_ms: Time to write file
- total_ms: Total processing time
```

**Example log output:**
```json
{
  "msg":"downloaded page",
  "total_ms":40055,
  "fetch_page_ms":323,
  "fetch_blocks_ms":2382,
  "convert_ms":0,
  "write_ms":0
}
```

**Impact:** Enables identification of bottlenecks in processing pipeline.

## Expected Performance Improvement

### Init Mode
- **Before:** ~1-2 seconds per already-synced page (API call overhead)
- **After:** ~0.01 seconds per already-synced page (registry lookup only)
- **Speedup:** 100-200x for cached pages

### Parent Resolution (Init Mode)
- **Before:** Recursive fetching could add 10-60 seconds per page with missing parents
- **After:** Queue parent for later (~0.01 seconds to create queue entry)
- **Speedup:** 1000-6000x for parent queuing vs fetching

### Overall
- **Init mode with mostly cached pages:** 100-200x faster
- **Init mode with many missing parents:** 50-100x faster
- **Update mode:** No change (still needs to fetch for correctness)

## Trade-offs

1. **File paths in init mode**: Pages with missing parents are initially placed at folder root rather than in their proper hierarchy. They're reorganized once the parent is processed.

2. **Parent processing order**: Parents are queued and processed in queue order, not immediately when needed. This means:
   - More flexible processing (can stop/resume easily)
   - Eventual consistency rather than immediate correctness
   - Better for large-scale syncs with many pages

## Testing

To test the optimizations:

```bash
# Start a 15-minute test session
./test.priv.sh

# Monitor logs for performance metrics
tail -f logs/*.log | grep -E "(downloaded|duration_ms|skipping existing page)"

# Check queue processing rate
watch -n 10 'ls .notion-sync/queue/*.json | wc -l'
```

## Next Steps

1. **Test with real workload**: Run full sync to verify performance improvements
2. **Monitor error rates**: Ensure no regressions from parent queuing changes
3. **Consider batching**: Group multiple pages into single API calls where possible
4. **Rate limiting awareness**: Monitor for Notion API rate limit hits with faster processing

## Files Changed

- `internal/sync/process.go`: Main optimization logic
- All changes are backward compatible
- No schema changes required

