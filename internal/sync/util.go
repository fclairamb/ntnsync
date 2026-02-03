package sync

import (
	"regexp"
	"strings"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// normalizePageID removes dashes from a page ID.
func normalizePageID(id string) string {
	return strings.ReplaceAll(id, "-", "")
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