package sync

import (
	"regexp"

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
