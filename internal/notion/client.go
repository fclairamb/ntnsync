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

// requestInfo holds metadata for a single API request (excluding context).
type requestInfo struct {
	method    string
	path      string
	pageID    string
	startTime time.Time
}

// logArgs returns base log arguments with optional pageID.
func (ri *requestInfo) logArgs(extra ...any) []any {
	args := []any{"method", ri.method, "path", ri.path}
	if ri.pageID != "" {
		args = append(args, "page_id", ri.pageID)
	}
	return append(args, extra...)
}

// do performs an HTTP request with rate limiting and retries.
func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	req, err := c.buildRequest(ctx, method, path, body)
	if err != nil {
		return err
	}

	reqInfo := &requestInfo{
		method:    method,
		path:      path,
		pageID:    PageIDFromContext(ctx),
		startTime: time.Now(),
	}

	c.logger.DebugContext(ctx, "API request", reqInfo.logArgs()...)

	return c.executeWithRetry(ctx, req, reqInfo, result)
}

// buildRequest creates an HTTP request with the appropriate headers.
func (c *Client) buildRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", c.apiVersion)
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// executeWithRetry executes the request with exponential backoff on rate limits.
func (c *Client) executeWithRetry(ctx context.Context, req *http.Request, reqInfo *requestInfo, result any) error {
	const maxRetries = 5
	backoff := time.Second

	for attempt := range maxRetries {
		done, err := c.executeRequest(ctx, req, reqInfo, result, attempt, &backoff)
		if done || err != nil {
			return err
		}
	}

	return apperrors.ErrMaxRetriesExceeded
}

// executeRequest performs a single request attempt. Returns (done, error).
func (c *Client) executeRequest(
	ctx context.Context, req *http.Request, reqInfo *requestInfo, result any, attempt int, backoff *time.Duration,
) (bool, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true, fmt.Errorf("do request: %w", err)
	}

	respBody, err := c.readAndCloseBody(ctx, resp)
	if err != nil {
		return true, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return c.handleRateLimit(ctx, reqInfo, attempt, backoff)
	}

	if resp.StatusCode >= httpStatusBadRequest {
		return true, c.parseErrorResponse(respBody, resp.StatusCode)
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return true, fmt.Errorf("unmarshal response: %w", err)
		}
	}

	c.logger.DebugContext(ctx, "API response",
		reqInfo.logArgs("status", resp.StatusCode, "duration", time.Since(reqInfo.startTime))...)

	return true, nil
}

// readAndCloseBody reads the response body and closes it.
func (c *Client) readAndCloseBody(ctx context.Context, resp *http.Response) ([]byte, error) {
	respBody, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		c.logger.WarnContext(ctx, "failed to close response body", "error", closeErr)
	}
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return respBody, nil
}

// handleRateLimit handles rate limit responses with backoff.
func (c *Client) handleRateLimit(
	ctx context.Context, reqInfo *requestInfo, attempt int, backoff *time.Duration,
) (bool, error) {
	c.logger.WarnContext(ctx, "rate limited, backing off",
		reqInfo.logArgs("attempt", attempt+1, "backoff", *backoff)...)

	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(*backoff):
		*backoff *= 2
		return false, nil
	}
}

// parseErrorResponse parses an API error response.
func (c *Client) parseErrorResponse(respBody []byte, statusCode int) error {
	var errResp APIError
	if err := json.Unmarshal(respBody, &errResp); err != nil {
		return apperrors.NewHTTPError(statusCode, string(respBody))
	}
	return &errResp
}
