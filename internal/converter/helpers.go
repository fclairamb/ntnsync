package converter

import (
	"strings"
)

const (
	// Filename constraints.
	maxFilenameLength = 100 // Maximum filename length before truncation
)

// SanitizeFilename makes a string safe for use as a filename.
// Only allows pattern [a-z][a-z0-9-]* (lowercase letters, numbers, hyphens).
// Must start with a letter.
func SanitizeFilename(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Build result with only allowed characters
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' || r == '/' || r == '\\' || r == ':' || r == '|' {
			// Replace separators with dash
			result.WriteRune('-')
		}
		// All other characters (including non-ASCII) are dropped
	}

	filename := result.String()

	// Collapse multiple dashes
	for strings.Contains(filename, "--") {
		filename = strings.ReplaceAll(filename, "--", "-")
	}

	// Trim dashes from ends
	filename = strings.Trim(filename, "-")

	// Ensure it starts with a letter
	for len(filename) > 0 && (filename[0] < 'a' || filename[0] > 'z') {
		filename = filename[1:]
	}

	// Truncate to reasonable length
	if len(filename) > maxFilenameLength {
		filename = filename[:maxFilenameLength]
	}

	// Ensure it doesn't end with a dash after truncation
	filename = strings.TrimRight(filename, "-")

	// Handle empty result
	if filename == "" {
		filename = defaultUntitledStr
	}

	return filename
}

// NormalizeID removes dashes from Notion IDs for consistent format.
func NormalizeID(id string) string {
	return strings.ReplaceAll(id, "-", "")
}
