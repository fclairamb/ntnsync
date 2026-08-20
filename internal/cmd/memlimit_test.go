package cmd

import (
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"testing"
)

// writeFile writes content to path, creating parent directories as needed, and fails
// the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %q: %v", path, err)
	}
}

// unsetGOMEMLIMIT ensures GOMEMLIMIT is unset for the duration of the test, restoring
// its previous value (set or unset) afterward.
func unsetGOMEMLIMIT(t *testing.T) {
	t.Helper()

	prev, wasSet := os.LookupEnv("GOMEMLIMIT")
	if err := os.Unsetenv("GOMEMLIMIT"); err != nil {
		t.Fatalf("unset GOMEMLIMIT: %v", err)
	}

	t.Cleanup(func() {
		if wasSet {
			if err := os.Setenv("GOMEMLIMIT", prev); err != nil {
				t.Errorf("restore GOMEMLIMIT: %v", err)
			}
		}
	})
}

func TestDetectCgroupMemoryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		v2Content  *string // nil = file does not exist
		v1Content  *string // nil = file does not exist
		wantLimit  int64
		wantDetect bool
	}{
		{
			name:       "v2 readable limit is used",
			v2Content:  strPtr("3221225472"),
			v1Content:  strPtr("999999999999999"), // would be unlimited-sentinel anyway
			wantLimit:  3221225472,
			wantDetect: true,
		},
		{
			name:       "v2 max falls back to v1",
			v2Content:  strPtr("max"),
			v1Content:  strPtr("1073741824"),
			wantLimit:  1073741824,
			wantDetect: true,
		},
		{
			name:       "v2 missing falls back to v1",
			v2Content:  nil,
			v1Content:  strPtr("1073741824"),
			wantLimit:  1073741824,
			wantDetect: true,
		},
		{
			name:       "both missing",
			v2Content:  nil,
			v1Content:  nil,
			wantDetect: false,
		},
		{
			name:       "v2 max and v1 missing",
			v2Content:  strPtr("max"),
			v1Content:  nil,
			wantDetect: false,
		},
		{
			name:       "v1 sentinel above ceiling is unlimited",
			v2Content:  nil,
			v1Content:  strPtr(strconv.FormatInt(math.MaxInt64, 10)),
			wantDetect: false,
		},
		{
			name:       "v2 above ceiling is unlimited",
			v2Content:  strPtr(strconv.FormatInt((1<<40)+1, 10)),
			v1Content:  nil,
			wantDetect: false,
		},
		{
			name:       "v2 unparseable falls back to v1",
			v2Content:  strPtr("not-a-number"),
			v1Content:  strPtr("1073741824"),
			wantLimit:  1073741824,
			wantDetect: true,
		},
		{
			name:       "v2 empty falls back to v1",
			v2Content:  strPtr(""),
			v1Content:  strPtr("1073741824"),
			wantLimit:  1073741824,
			wantDetect: true,
		},
		{
			name:       "v2 negative is rejected, v1 missing",
			v2Content:  strPtr("-1"),
			v1Content:  nil,
			wantDetect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			v2Path := filepath.Join(dir, "memory.max")
			v1Path := filepath.Join(dir, "memory.limit_in_bytes")

			if tt.v2Content != nil {
				writeFile(t, v2Path, *tt.v2Content)
			}
			if tt.v1Content != nil {
				writeFile(t, v1Path, *tt.v1Content)
			}

			limit, ok := detectCgroupMemoryLimit(v2Path, v1Path)
			if ok != tt.wantDetect {
				t.Fatalf("detectCgroupMemoryLimit() ok = %v, want %v", ok, tt.wantDetect)
			}
			if ok && limit != tt.wantLimit {
				t.Fatalf("detectCgroupMemoryLimit() limit = %d, want %d", limit, tt.wantLimit)
			}
		})
	}
}

func TestReadCgroupMemoryLimit_MissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, ok := readCgroupMemoryLimit(filepath.Join(dir, "does-not-exist"))
	if ok {
		t.Fatalf("readCgroupMemoryLimit() ok = true for missing file, want false")
	}
}

func TestSetMemoryLimitFromCgroup_GOMEMLIMITAlreadySet(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "1GiB")

	dir := t.TempDir()
	v2Path := filepath.Join(dir, "memory.max")
	writeFile(t, v2Path, "3221225472")
	v1Path := filepath.Join(dir, "memory.limit_in_bytes")

	before := debug.SetMemoryLimit(-1) // read current limit without changing it

	setMemoryLimitFromCgroup(v2Path, v1Path)

	after := debug.SetMemoryLimit(-1)
	if after != before {
		t.Fatalf("setMemoryLimitFromCgroup() changed memory limit from %d to %d, want no change (GOMEMLIMIT was set)", before, after)
	}
}

// TestSetMemoryLimitFromCgroup_AppliesEightyFivePercent mutates the process-global
// runtime memory limit, so it must not run concurrently with other tests that do the
// same.
//
//nolint:paralleltest // see comment above
func TestSetMemoryLimitFromCgroup_AppliesEightyFivePercent(t *testing.T) {
	unsetGOMEMLIMIT(t)

	dir := t.TempDir()
	v2Path := filepath.Join(dir, "memory.max")
	writeFile(t, v2Path, "3221225472")
	v1Path := filepath.Join(dir, "memory.limit_in_bytes")

	defer debug.SetMemoryLimit(debug.SetMemoryLimit(-1)) // restore whatever was set before

	setMemoryLimitFromCgroup(v2Path, v1Path)

	got := debug.SetMemoryLimit(-1)
	// 85% of 3221225472, i.e. floor(3221225472 * 85 / 100). Note: the spec's own
	// worked example states 2737418240, which is arithmetically incorrect (true 85%
	// of 3221225472 is 2738041651.2); this test uses the mathematically correct value.
	want := int64(2738041651)
	if got != want {
		t.Fatalf("SetMemoryLimit() = %d, want %d", got, want)
	}
}

// TestSetMemoryLimitFromCgroup_NoUsableLimitIsNoOp mutates the process-global runtime
// memory limit, so it must not run concurrently with other tests that do the same.
//
//nolint:paralleltest // see comment above
func TestSetMemoryLimitFromCgroup_NoUsableLimitIsNoOp(t *testing.T) {
	unsetGOMEMLIMIT(t)

	dir := t.TempDir()
	v2Path := filepath.Join(dir, "memory.max")
	v1Path := filepath.Join(dir, "memory.limit_in_bytes")
	// Neither file exists.

	before := debug.SetMemoryLimit(-1)

	setMemoryLimitFromCgroup(v2Path, v1Path)

	after := debug.SetMemoryLimit(-1)
	if after != before {
		t.Fatalf("setMemoryLimitFromCgroup() changed memory limit from %d to %d, want no change (no usable cgroup limit)", before, after)
	}
}

func strPtr(s string) *string {
	return &s
}
