package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// pageIDKey is the context key for storing the current page ID.
	pageIDKey contextKey = "pageID"
)

// WithPageID returns a new context with the page ID stored.
func WithPageID(ctx context.Context, pageID string) context.Context {
	return context.WithValue(ctx, pageIDKey, pageID)
}

// PageIDFromContext extracts the page ID from context, returns empty string if not set.
func PageIDFromContext(ctx context.Context) string {
	if v := ctx.Value(pageIDKey); v != nil {
		if pageID, ok := v.(string); ok {
			return pageID
		}
	}
	return ""
}

const (
	// BaseURL is the Notion API base URL.
	BaseURL = "https://api.notion.com/v1"
	// APIVersion is the Notion API version to use.
	APIVersion = "2022-06-28"

	// HTTP client configuration.
	httpTimeout = 30 * time.Second // Timeout for HTTP requests

	// Rate limiting configuration (~3 requests/second).
	rateLimitInterval = 350 * time.Millisecond

	// HTTP status codes.
	httpStatusBadRequest = 400 // First status code indicating an error
)

// Client is a Notion API client with rate limiting.
type Client struct {
	httpClient  *http.Client
	token       string
	rateLimiter *rate.Limiter
	baseURL     string
	apiVersion  string
	logger      *slog.Logger
}

// ClientOption configures the client.
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(client *Client) {
		client.httpClient = c
	}
}

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) ClientOption {
	return func(client *Client) {
		client.logger = l
	}
}

// WithBaseURL sets a custom base URL (useful for testing).
func WithBaseURL(url string) ClientOption {
	return func(client *Client) {
		client.baseURL = url
	}
}

// NewClient creates a new Notion API client.
func NewClient(token string, opts ...ClientOption) *Client {
	client := &Client{
		httpClient:  &http.Client{Timeout: httpTimeout},
		token:       token,
		rateLimiter: rate.NewLimiter(rate.Every(rateLimitInterval), 1), // ~3 req/s
		baseURL:     BaseURL,
		apiVersion:  APIVersion,
		logger:      slog.Default(),
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// do performs an HTTP request with rate limiting and retries.
//
//nolint:funlen // HTTP client with retry logic and error handling
func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	// Wait for rate limiter
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", c.apiVersion)
	req.Header.Set("Content-Type", "application/json")

	logArgs := []any{"method", method, "path", path}
	if pageID := PageIDFromContext(ctx); pageID != "" {
		logArgs = append(logArgs, "page_id", pageID)
	}
	c.logger.DebugContext(ctx, "API request", logArgs...)
	startTime := time.Now()

	// Retry with exponential backoff on rate limit
	maxRetries := 5
	backoff := time.Second

	for attempt := range maxRetries {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("do request: %w", err)
		}

		respBody, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.WarnContext(ctx, "failed to close response body", "error", closeErr)
		}
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			rateLimitArgs := []any{"attempt", attempt + 1, "backoff", backoff}
			if pageID := PageIDFromContext(ctx); pageID != "" {
				rateLimitArgs = append(rateLimitArgs, "page_id", pageID)
			}
			c.logger.WarnContext(ctx, "rate limited, backing off", rateLimitArgs...)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}

		if resp.StatusCode >= httpStatusBadRequest {
			var errResp APIError
			if err := json.Unmarshal(respBody, &errResp); err != nil {
				return apperrors.NewHTTPError(resp.StatusCode, string(respBody))
			}
			return &errResp
		}

		if result != nil {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("unmarshal response: %w", err)
			}
		}

		respLogArgs := []any{"method", method, "path", path, "status", resp.StatusCode, "duration", time.Since(startTime)}
		if pageID := PageIDFromContext(ctx); pageID != "" {
			respLogArgs = append(respLogArgs, "page_id", pageID)
		}
		c.logger.DebugContext(ctx, "API response", respLogArgs...)

		return nil
	}

	return apperrors.ErrMaxRetriesExceeded
}
