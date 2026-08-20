package sync

import (
	"regexp"
	"strings"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/notion"
)

// normalizePageID removes dashes from a page ID so registry keys are canonical.
func normalizePageID(id string) string {
	return notion.NormalizeID(id)
}

// denormalizePageID re-inserts UUID dashes into a normalized page ID. It is used
// to locate legacy registry files that were written under the dashed form before
// IDs were normalized on every code path.
func denormalizePageID(id string) string {
	return notion.DenormalizeID(id)
}

// validateFolderName validates a folder name.
func validateFolderName(folder string) error {
	if folder == "" {
		return apperrors.ErrFolderNameEmpty
	}

	// Only allow lowercase alphanumeric and hyphens
	matched, err := regexp.MatchString("^[a-z0-9-]+$", folder)
	if err != nil {
		return err
	}
	if !matched {
		return apperrors.ErrFolderNameInvalid
	}

	return nil
}

// stripURLQuery removes the query string from a URL.
//
// Notion serves attachments through pre-signed S3 URLs whose query carries ~2 KB
// of credentials, signature and expiry. That payload is never read back and
// rotates on every fetch, so storing it churns the registry record even when the
// file itself has not changed. Only the bare object path is kept.
func stripURLQuery(rawURL string) string {
	bare, _, _ := strings.Cut(rawURL, "?")
	return bare
}
