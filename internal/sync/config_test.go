package sync

import "testing"

// TestGetMaxFileSize_Default verifies the default max file size resolves to
// 512KB (524288 bytes) when NTN_MAX_FILE_SIZE is unset.
func TestGetMaxFileSize_Default(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv
	ResetConfig()
	t.Setenv("NTN_MAX_FILE_SIZE", "")

	const expected = 524288
	if got := getMaxFileSize(); got != expected {
		t.Errorf("getMaxFileSize() = %d, expected %d", got, expected)
	}

	ResetConfig()
}

// TestGetMaxFileSize_Override verifies NTN_MAX_FILE_SIZE still overrides the
// default, e.g. to restore the previous 5MB behaviour.
func TestGetMaxFileSize_Override(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv
	ResetConfig()
	t.Setenv("NTN_MAX_FILE_SIZE", "5MB")

	const expected = 5 * bytesPerMB
	if got := getMaxFileSize(); got != expected {
		t.Errorf("getMaxFileSize() = %d, expected %d", got, expected)
	}

	ResetConfig()
}
