// Package webhook provides HTTP webhook handling for Notion events.
package webhook

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultWebhookPort is the default HTTP port for the webhook server.
	defaultWebhookPort = 8080
)

// ServerConfig holds configuration for the webhook server.
type ServerConfig struct {
	Port      int           // HTTP port to listen on (NTN_WEBHOOK_PORT, default 8080)
	Path      string        // Webhook endpoint path (NTN_WEBHOOK_PATH, default /webhooks/notion)
	Secret    string        // Webhook secret for signature verification (NTN_WEBHOOK_SECRET, optional)
	AutoSync  bool          // Automatically run sync after queuing webhook events (NTN_WEBHOOK_AUTO_SYNC, default true)
	SyncDelay time.Duration // Delay before processing queue (NTN_WEBHOOK_SYNC_DELAY, default 0)
}

// LoadConfigFromEnv loads webhook configuration from environment variables.
func LoadConfigFromEnv() *ServerConfig {
	cfg := &ServerConfig{
		Port:     defaultWebhookPort,
		Path:     "/webhooks/notion",
		Secret:   os.Getenv("NTN_WEBHOOK_SECRET"),
		AutoSync: true,
	}

	if portStr := os.Getenv("NTN_WEBHOOK_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
			cfg.Port = port
		}
	}

	if path := os.Getenv("NTN_WEBHOOK_PATH"); path != "" {
		cfg.Path = path
	}

	if autoSyncStr := os.Getenv("NTN_WEBHOOK_AUTO_SYNC"); autoSyncStr != "" {
		cfg.AutoSync = parseBoolEnv(autoSyncStr)
	}

	if syncDelayStr := os.Getenv("NTN_WEBHOOK_SYNC_DELAY"); syncDelayStr != "" {
		if d, err := time.ParseDuration(syncDelayStr); err == nil && d >= 0 {
			cfg.SyncDelay = d
		}
	}

	return cfg
}

// IsValid returns true if the configuration is valid.
// Secret is optional (signature verification is skipped if not set).
func (c *ServerConfig) IsValid() bool {
	return c.Port > 0 && c.Path != ""
}

// parseBoolEnv parses a boolean environment variable value.
func parseBoolEnv(val string) bool {
	val = strings.ToLower(val)
	return val == "true" || val == "1" || val == "yes"
}
