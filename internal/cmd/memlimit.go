package cmd

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	// cgroupV2MemoryMaxPath is the cgroup v2 file reporting the memory limit in bytes,
	// or the literal "max" when unlimited.
	cgroupV2MemoryMaxPath = "/sys/fs/cgroup/memory.max"
	// cgroupV1MemoryLimitPath is the cgroup v1 file reporting the memory limit in bytes.
	// It reports a sentinel near math.MaxInt64 when unlimited.
	cgroupV1MemoryLimitPath = "/sys/fs/cgroup/memory/memory.limit_in_bytes"

	// memLimitFactorNumerator and memLimitFactorDenominator apply an 85% factor to the
	// detected cgroup memory limit before handing it to the Go runtime.
	memLimitFactorNumerator   = 85
	memLimitFactorDenominator = 100

	// unlimitedCeiling is the sane ceiling above which a reported cgroup limit is treated
	// as "unlimited" (cgroup v1 reports a sentinel near math.MaxInt64 for unlimited).
	unlimitedCeiling = 1 << 40 // 1 TiB
)

// SetMemoryLimitFromCgroup sets GOMEMLIMIT to 85% of the cgroup memory limit, unless
// GOMEMLIMIT is already set in the environment (an explicit operator setting always
// wins) or no usable cgroup limit can be determined. It is a no-op on platforms without
// cgroup files (e.g. macOS, Windows), since the "unreadable -> do nothing" rule covers
// their absence.
func SetMemoryLimitFromCgroup() {
	setMemoryLimitFromCgroup(cgroupV2MemoryMaxPath, cgroupV1MemoryLimitPath)
}

// setMemoryLimitFromCgroup is the injectable-path implementation used by tests.
func setMemoryLimitFromCgroup(cgroupV2Path, cgroupV1Path string) {
	if _, ok := os.LookupEnv("GOMEMLIMIT"); ok {
		// Explicit operator setting; the Go runtime already applied it.
		return
	}

	limit, ok := detectCgroupMemoryLimit(cgroupV2Path, cgroupV1Path)
	if !ok {
		return
	}

	applied := limit * memLimitFactorNumerator / memLimitFactorDenominator
	debug.SetMemoryLimit(applied)
	slog.Info("set GOMEMLIMIT from cgroup memory limit", "detected_bytes", limit, "applied_bytes", applied)
}

// detectCgroupMemoryLimit reads the cgroup v2 memory limit, falling back to cgroup v1.
// It returns false if no usable (bounded) limit could be determined.
func detectCgroupMemoryLimit(cgroupV2Path, cgroupV1Path string) (int64, bool) {
	if limit, ok := readCgroupMemoryLimit(cgroupV2Path); ok {
		return limit, true
	}

	return readCgroupMemoryLimit(cgroupV1Path)
}

// readCgroupMemoryLimit reads and parses a single cgroup memory limit file. It returns
// false if the file is missing, unreadable, contains "max", fails to parse, or reports
// a value above the sane unlimited ceiling.
func readCgroupMemoryLimit(path string) (int64, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a fixed cgroup file or test-injected
	if err != nil {
		return 0, false
	}

	text := strings.TrimSpace(string(data))
	if text == "" || text == "max" {
		return 0, false
	}

	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 || value > unlimitedCeiling {
		return 0, false
	}

	return value, true
}
