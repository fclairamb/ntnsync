# Accent Transliteration for Filename Generation

## Problem

When generating markdown filenames from Notion page titles, accented characters are currently dropped instead of being transliterated to their ASCII equivalents.

**Current behavior:**
- "échanges à venirs" → "changes-venir.md" (incorrect)
- "café" → "caf.md" (incorrect)

**Expected behavior:**
- "échanges à venirs" → "echanges-a-venirs.md" (correct)
- "café" → "cafe.md" (correct)

## Root Cause

In `internal/converter/helpers.go`, the `SanitizeFilename()` function (lines 15-60) has this logic:

```go
// All other characters (including non-ASCII) are dropped
```

Non-ASCII characters like `é`, `à`, `ñ`, `ü` are simply removed rather than converted to their base ASCII form (`e`, `a`, `n`, `u`).

## Proposed Solution

Add Unicode transliteration before the existing sanitization logic. This should convert accented characters to their ASCII equivalents:

| Input | Output |
|-------|--------|
| é, è, ê, ë | e |
| à, â, ä | a |
| ù, û, ü | u |
| ô, ö | o |
| î, ï | i |
| ç | c |
| ñ | n |
| ß | ss |
| æ | ae |
| œ | oe |

### Implementation

Use `golang.org/x/text/transform` with `runes.Remove(runes.In(unicode.Mn))` to normalize Unicode and strip combining marks:

```go
import (
    "unicode"
    "golang.org/x/text/transform"
    "golang.org/x/text/unicode/norm"
    "golang.org/x/text/runes"
)

func transliterate(s string) string {
    t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
    result, _, _ := transform.String(t, s)
    return result
}
```

This uses Unicode Normalization Form D (NFD) to decompose characters like `é` into `e` + combining acute accent, then removes the combining marks.

This approach:
- Uses Go's extended standard library (`golang.org/x/text`)
- Adds no new third-party dependencies
- Handles most common accented characters correctly

## Files to Modify

1. **`internal/converter/helpers.go`**
   - Add `transliterate()` helper function
   - Call it at the start of `SanitizeFilename()` before other processing

2. **`internal/converter/helpers_test.go`**
   - Update `TestSanitizeFilename_UnicodeChars` to expect transliteration instead of removal
   - Add test cases for various accented characters

## Test Cases

```go
// Accented characters should be transliterated
{"échanges à venirs", "echanges-a-venirs"},
{"café", "cafe"},
{"naïve", "naive"},
{"piñata", "pinata"},
{"über", "uber"},
{"façade", "facade"},
{"résumé", "resume"},
{"Ångström", "angstrom"},

// Emojis and non-Latin scripts should still be removed
{"my🎉page", "mypage"},
{"test页面", "test"},
```

## Migration Notes

- Existing files will **not** be renamed automatically
- New pages and renamed pages will use the new transliteration
- The registry tracks pages by ID, so path changes don't affect sync behavior
- Consider documenting this as a minor behavior change in release notes
