# File Naming Conflict Resolution

## Problem

When downloading files from Notion, two different files can have the same filename. Currently, if a file with the same name already exists locally, it could be overwritten even though it's a different file (different Notion file ID).

Example scenario:
1. Page A has an image named `diagram.png` (file ID: `abc123`)
2. Page B has a different image also named `diagram.png` (file ID: `def456`)
3. Both pages are in the same folder, so files go to the same `files/` directory
4. The second download overwrites the first

## Current Behavior

Files are downloaded to: `{page_dir}/{page_name}/files/{sanitized_filename}`

The `FileRegistry` tracks files by their Notion file ID and prevents re-downloading the same file. However, it doesn't handle the case where two different files have the same local filename.

Each downloaded file has a `.meta.json` manifest:
```json
{
  "file_id": "7d399803385144",
  "parent_page_id": "abc123def456",
  "downloaded_at": "2026-01-25T10:00:00Z"
}
```

## Solution

Before saving a downloaded file, check if a file with the same name already exists:

1. If no file exists at the target path → save normally
2. If file exists, read its `.meta.json`:
   - If same `file_id` → file already downloaded, skip (current behavior)
   - If different `file_id` → naming conflict, append suffix
3. If file exists but no `.meta.json` → treat as conflict (legacy file)

### Conflict Resolution

Use the same pattern as page naming conflicts: append a 4-character suffix from the file ID.

```
files/
  diagram.png           # First file (file_id: abc123...)
  diagram.meta.json
  diagram-def4.png      # Conflicting file (file_id: def456...)
  diagram-def4.meta.json
```

### Algorithm

```go
func resolveFileConflict(targetDir, filename, fileID string) string {
    base := filenameWithoutExt(filename)  // "diagram"
    ext := filepath.Ext(filename)          // ".png"

    candidate := filename
    for i := 0; i < maxAttempts; i++ {
        fullPath := filepath.Join(targetDir, candidate)
        metaPath := fullPath + ".meta.json"

        if !fileExists(fullPath) {
            // No conflict
            return candidate
        }

        // File exists, check meta
        if meta, err := readFileManifest(metaPath); err == nil {
            if meta.FileID == fileID {
                // Same file, already downloaded
                return candidate
            }
        }

        // Different file or no meta - add suffix
        shortID := fileID[:4]
        candidate = fmt.Sprintf("%s-%s%s", base, shortID, ext)
    }

    return candidate
}
```

## Implementation

### Changes to `internal/sync/file.go`

1. Add `resolveFileConflict()` function
2. Modify `processFileURL()` to use conflict resolution before downloading
3. Update `FileRegistry.FilePath` to store the resolved filename

### FileRegistry Update

The `FileRegistry` already stores the resolved `FilePath`. No schema changes needed, but ensure the path is determined **before** saving to registry.

### Edge Cases

1. **Legacy files without .meta.json**: Treat as conflict, use suffix for new file
2. **Same file, different URL**: Notion can serve the same file from different S3 URLs. The file ID (extracted from URL path) remains the same, so this is handled correctly.
3. **Filename sanitization conflicts**: After sanitizing, two different filenames could become identical (e.g., `Diagram (1).png` and `Diagram (2).png` both become `diagram.png`). The conflict resolution handles this.

## Testing

1. Download a file, verify `.meta.json` created
2. Download different file with same name, verify suffix added
3. Re-sync same file, verify no duplicate created
4. Test with files that sanitize to same name
