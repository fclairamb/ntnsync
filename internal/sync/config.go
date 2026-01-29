package sync

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds sync-related configuration loaded from environment variables.
type Config struct {
	// BlockDepth is the maximum depth for block discovery (0 = unlimited).
	BlockDepth int
	// QueueDelay is the delay between processing queue files.
	QueueDelay time.Duration
	// MaxFileSize is the maximum file size to download in bytes.
	MaxFileSize int64
}

// globalConfig is the singleton config instance.
var globalConfig *Config

// LoadConfig loads configuration from environment variables.
// It should be called once at application startup.
func LoadConfig() error {
	globalConfig = &Config{
		BlockDepth:  parseIntEnv(os.Getenv("NTN_BLOCK_DEPTH"), 0),
		QueueDelay:  parseDurationEnv(os.Getenv("NTN_QUEUE_DELAY"), 0),
		MaxFileSize: parseFileSizeEnv(os.Getenv("NTN_MAX_FILE_SIZE"), defaultMaxFileSize),
	}

	return nil
}

// GetConfig returns the global configuration.
// If not loaded, it loads with defaults.
func GetConfig() *Config {
	if globalConfig == nil {
		// Load config if not already loaded (lazy initialization)
		_ = LoadConfig()
	}
	return globalConfig
}

// ResetConfig resets the global configuration, forcing a reload on next access.
// This is primarily used for testing.
func ResetConfig() {
	globalConfig = nil
}

// parseIntEnv parses an integer from a string, returning defaultVal on error.
func parseIntEnv(val string, defaultVal int) int {
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil || i < 0 {
		return defaultVal
	}
	return i
}

// parseDurationEnv parses a duration from a string, returning defaultVal on error.
func parseDurationEnv(val string, defaultVal time.Duration) time.Duration {
	if val == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return defaultVal
	}
	return d
}

// parseFileSizeEnv parses a file size from a string (e.g., "5MB", "100KB", "1GB").
// Returns defaultVal if not set or invalid.
func parseFileSizeEnv(val string, defaultVal int64) int64 {
	if val == "" || val == "0" {
		return defaultVal
	}

	// Try parsing as plain bytes
	if bytes, err := strconv.ParseInt(val, 10, 64); err == nil {
		return bytes
	}

	// Parse with unit suffix
	val = strings.ToUpper(strings.TrimSpace(val))

	units := map[string]int64{
		"B":  1,
		"KB": bytesPerKB,
		"MB": bytesPerMB,
		"GB": bytesPerGB,
	}

	for suffix, multiplier := range units {
		if numStr, found := strings.CutSuffix(val, suffix); found {
			numStr = strings.TrimSpace(numStr)
			if num, err := strconv.ParseFloat(numStr, 64); err == nil {
				return int64(num * float64(multiplier))
			}
		}
	}

	return defaultVal
}
