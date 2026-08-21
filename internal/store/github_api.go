package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

const (
	// gitHubAPIBaseURL is the public GitHub REST API root.
	gitHubAPIBaseURL = "https://api.github.com"

	// gitHubAPIVersion pins the REST API version we speak.
	gitHubAPIVersion = "2022-11-28"

	// gitHubAcceptJSON is the media type for the GitHub REST API.
	gitHubAcceptJSON = "application/vnd.github+json"

	// gitHubRateFloor is the number of remaining core requests below which we
	// stop issuing requests and wait for the window to reset. Blocking beats
	// failing a sync half-way through.
	gitHubRateFloor = 50

	// gitHubMaxAttempts bounds retries of a single HTTP request.
	gitHubMaxAttempts = 4

	// gitHubInitialBackoff is the first retry delay for transient failures.
	gitHubInitialBackoff = 1 * time.Second

	// gitHubBackoffFactor multiplies the delay between retries.
	gitHubBackoffFactor = 2

	// gitHubMaxRateWait caps how long we are willing to block on a rate-limit
	// reset before giving up, so a bad clock cannot hang the process forever.
	gitHubMaxRateWait = 90 * time.Minute

	// gitHubRequestTimeout bounds a single HTTP round trip.
	gitHubRequestTimeout = 60 * time.Second

	// gitHubBlobMode is the git file mode for a regular non-executable file.
	gitHubBlobMode = "100644"

	// gitHubTreeMode is the git file mode for a subtree.
	gitHubTreeMode = "040000"

	// gitHubTypeBlob / gitHubTypeTree are git object types in tree entries.
	gitHubTypeBlob = "blob"
	gitHubTypeTree = "tree"

	// gitHubEncodingBase64 is the only blob encoding we send or accept.
	gitHubEncodingBase64 = "base64"

	// httpStatusServerErrorFloor is the lowest 5xx status code.
	httpStatusServerErrorFloor = 500
)

// gitHubAPIError is a non-2xx response from the GitHub API.
type gitHubAPIError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
	Remaining  int
	Reset      time.Time
}

// Error implements the error interface.
func (e *gitHubAPIError) Error() string {
	return fmt.Sprintf("github api %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// isGitHubStatus reports whether err is a GitHub API error with the given status.
func isGitHubStatus(err error, status int) bool {
	var apiErr *gitHubAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == status
}

// sleepFunc pauses for the given duration or until the context is canceled.
// It is a field so tests can observe waits without real time passing.
type sleepFunc func(ctx context.Context, d time.Duration) error

// realSleep waits for d, honoring context cancellation.
func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// rateBudget is the last seen state of the core rate-limit window.
type rateBudget struct {
	Limit     int
	Remaining int
	Reset     time.Time
	Known     bool
}

// githubAPI is a minimal hand-rolled client for the GitHub Git Data API.
// It deliberately depends on nothing but the standard library so tests can
// inject a stub http.RoundTripper.
type githubAPI struct {
	baseURL    string
	owner      string
	repo       string
	token      string
	httpClient *http.Client
	logger     *slog.Logger
	sleep      sleepFunc
	now        func() time.Time

	mu   sync.Mutex
	rate rateBudget
}

// githubAPIOption configures a githubAPI.
type githubAPIOption func(*githubAPI)

// withGitHubTransport overrides the HTTP transport, for tests.
func withGitHubTransport(rt http.RoundTripper) githubAPIOption {
	return func(a *githubAPI) {
		a.httpClient = &http.Client{Transport: rt, Timeout: gitHubRequestTimeout}
	}
}

// withGitHubSleep overrides the sleep function, for tests.
func withGitHubSleep(fn sleepFunc) githubAPIOption {
	return func(a *githubAPI) {
		a.sleep = fn
	}
}

// withGitHubClock overrides the clock, for tests.
func withGitHubClock(fn func() time.Time) githubAPIOption {
	return func(a *githubAPI) {
		a.now = fn
	}
}

// withGitHubBaseURL overrides the API root, for tests.
func withGitHubBaseURL(base string) githubAPIOption {
	return func(a *githubAPI) {
		a.baseURL = strings.TrimSuffix(base, "/")
	}
}

// withGitHubLogger sets the logger.
func withGitHubLogger(logger *slog.Logger) githubAPIOption {
	return func(a *githubAPI) {
		a.logger = logger
	}
}

// newGitHubAPI builds a client for one owner/repo pair.
func newGitHubAPI(owner, repo, token string, opts ...githubAPIOption) *githubAPI {
	api := &githubAPI{
		baseURL:    gitHubAPIBaseURL,
		owner:      owner,
		repo:       repo,
		token:      token,
		httpClient: &http.Client{Timeout: gitHubRequestTimeout},
		logger:     slog.Default(),
		sleep:      realSleep,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(api)
	}
	return api
}

// repoPath builds a path under /repos/{owner}/{repo}.
func (a *githubAPI) repoPath(suffix string) string {
	return "/repos/" + a.owner + "/" + a.repo + suffix
}

// rateSnapshot returns the last observed rate-limit budget.
func (a *githubAPI) rateSnapshot() rateBudget {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rate
}

// observeRate records the rate-limit headers of a response.
func (a *githubAPI) observeRate(header http.Header) rateBudget {
	remainingStr := header.Get("X-Ratelimit-Remaining")
	if remainingStr == "" {
		return a.rateSnapshot()
	}

	remaining, err := strconv.Atoi(remainingStr)
	if err != nil {
		return a.rateSnapshot()
	}

	budget := rateBudget{Remaining: remaining, Known: true}
	if limit, convErr := strconv.Atoi(header.Get("X-Ratelimit-Limit")); convErr == nil {
		budget.Limit = limit
	}
	if reset, convErr := strconv.ParseInt(header.Get("X-Ratelimit-Reset"), 10, 64); convErr == nil {
		budget.Reset = time.Unix(reset, 0)
	}

	a.mu.Lock()
	a.rate = budget
	a.mu.Unlock()

	return budget
}

// clearBudget forgets the observed rate-limit window.
func (a *githubAPI) clearBudget() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.rate.Known = false
}

// waitForBudget blocks while the remaining request budget is at or below the
// floor, rather than letting the sync fail with a rate-limit error.
func (a *githubAPI) waitForBudget(ctx context.Context) error {
	a.mu.Lock()
	budget := a.rate
	shouldWait := budget.Known && budget.Remaining <= gitHubRateFloor
	if shouldWait {
		// Clear the budget so a single window is only waited out once.
		a.rate.Known = false
	}
	a.mu.Unlock()

	if !shouldWait {
		return nil
	}

	wait := budget.Reset.Sub(a.now())
	if wait <= 0 {
		return nil
	}
	if wait > gitHubMaxRateWait {
		wait = gitHubMaxRateWait
	}

	a.logger.WarnContext(ctx, "github rate limit budget exhausted, waiting for reset",
		"remaining", budget.Remaining,
		"floor", gitHubRateFloor,
		"wait", wait)

	return a.sleep(ctx, wait)
}

// logRateBudget reports the current core rate-limit budget at INFO level.
func (a *githubAPI) logRateBudget(ctx context.Context) {
	budget := a.rateSnapshot()
	if !budget.Known {
		return
	}
	a.logger.InfoContext(ctx, "github rate limit budget",
		"remaining", budget.Remaining,
		"limit", budget.Limit,
		"reset", budget.Reset.UTC().Format(time.RFC3339))
}

// do performs one API call with retries for transient and rate-limit failures.
// out may be nil when the response body is not needed.
func (a *githubAPI) do(ctx context.Context, method, path string, body, out any) error {
	payload, err := encodeBody(body)
	if err != nil {
		return err
	}

	delay := gitHubInitialBackoff

	var lastErr error

	for attempt := range gitHubMaxAttempts {
		if waitErr := a.waitForBudget(ctx); waitErr != nil {
			return waitErr
		}

		err = a.attempt(ctx, method, path, payload, out)
		if err == nil {
			return nil
		}

		lastErr = err

		retryDelay, retryable := a.retryDelay(err, delay)
		if !retryable || attempt == gitHubMaxAttempts-1 {
			return err
		}

		a.logger.WarnContext(ctx, "github api request failed, retrying",
			"method", method, "path", path, "delay", retryDelay, "error", err)

		if sleepErr := a.sleep(ctx, retryDelay); sleepErr != nil {
			return sleepErr
		}

		delay *= gitHubBackoffFactor
	}

	return lastErr
}

// encodeBody marshals a request body, returning nil for a nil body.
func encodeBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode github request body: %w", err)
	}
	return payload, nil
}

// retryDelay decides whether err is worth retrying and how long to wait.
func (a *githubAPI) retryDelay(err error, backoff time.Duration) (time.Duration, bool) {
	var apiErr *gitHubAPIError
	if !errors.As(err, &apiErr) {
		// Network-level failures are worth one more try.
		return backoff, true
	}

	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests,
		apiErr.StatusCode == http.StatusForbidden && apiErr.Remaining == 0:
		// The wait below already covers this window, so drop the observed
		// budget to avoid waiting for the same reset twice.
		a.clearBudget()

		wait := apiErr.Reset.Sub(a.now())
		if wait <= 0 {
			wait = backoff
		}
		if wait > gitHubMaxRateWait {
			wait = gitHubMaxRateWait
		}
		return wait, true
	case apiErr.StatusCode >= httpStatusServerErrorFloor:
		return backoff, true
	default:
		return 0, false
	}
}

// attempt performs a single HTTP round trip and decodes the response.
func (a *githubAPI) attempt(ctx context.Context, method, path string, payload []byte, out any) error {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build github request %s %s: %w", method, path, err)
	}

	req.Header.Set("Accept", gitHubAcceptJSON)
	req.Header.Set("X-Github-Api-Version", gitHubAPIVersion)
	req.Header.Set("User-Agent", "ntnsync")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	budget := a.observeRate(resp.Header)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read github response %s %s: %w", method, path, err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &gitHubAPIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Message:    extractGitHubMessage(respBody),
			Remaining:  budget.Remaining,
			Reset:      budget.Reset,
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode github response %s %s: %w", method, path, err)
	}

	return nil
}

// extractGitHubMessage pulls the "message" field out of an error body.
func extractGitHubMessage(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	return strings.TrimSpace(string(body))
}

// gitHubRefResponse is the payload of a git ref lookup.
type gitHubRefResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// gitHubCommitResponse is the payload of a git commit lookup or creation.
type gitHubCommitResponse struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

// gitHubTreeEntry is one entry of a tree listing.
type gitHubTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// gitHubTreeResponse is the payload of a tree lookup or creation.
type gitHubTreeResponse struct {
	SHA       string            `json:"sha"`
	Tree      []gitHubTreeEntry `json:"tree"`
	Truncated bool              `json:"truncated"`
}

// gitHubTreeRequestEntry is one entry of a tree creation request.
// SHA is a pointer because a nil (JSON null) sha is how a path is deleted,
// so the field must never be omitted from the payload.
type gitHubTreeRequestEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}

// gitHubBlobResponse is the payload of a blob lookup or creation.
type gitHubBlobResponse struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// gitHubAuthor is the author/committer identity of a created commit.
type gitHubAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// getRef reads the commit SHA a branch points at. found is false when the
// branch does not exist yet (a fresh repository or a new queue branch).
//
//nolint:nonamedreturns // named for documentation
func (a *githubAPI) getRef(ctx context.Context, branch string) (sha string, found bool, err error) {
	var out gitHubRefResponse

	path := a.repoPath("/git/ref/heads/" + url.PathEscape(branch))
	if err := a.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		if isGitHubStatus(err, http.StatusNotFound) || isGitHubStatus(err, http.StatusConflict) {
			return "", false, nil
		}
		return "", false, err
	}

	return out.Object.SHA, out.Object.SHA != "", nil
}

// getCommitTree returns the root tree SHA of a commit.
func (a *githubAPI) getCommitTree(ctx context.Context, commitSHA string) (string, error) {
	var out gitHubCommitResponse

	path := a.repoPath("/git/commits/" + commitSHA)
	if err := a.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}

	return out.Tree.SHA, nil
}

// getTree lists one tree, non-recursively.
//
// This never passes recursive=1: on a repository of the size this backend
// targets the recursive form comes back truncated and silently incomplete,
// which would make reads wrong rather than slow.
func (a *githubAPI) getTree(ctx context.Context, treeSHA string) ([]gitHubTreeEntry, error) {
	var out gitHubTreeResponse

	path := a.repoPath("/git/trees/" + treeSHA)
	if err := a.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}

	if out.Truncated {
		return nil, fmt.Errorf("%w: tree %s", apperrors.ErrGitHubTreeTruncated, treeSHA)
	}

	return out.Tree, nil
}

// getBlob downloads and decodes one blob.
func (a *githubAPI) getBlob(ctx context.Context, blobSHA string) ([]byte, error) {
	var out gitHubBlobResponse

	path := a.repoPath("/git/blobs/" + blobSHA)
	if err := a.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}

	if out.Encoding != "" && out.Encoding != gitHubEncodingBase64 {
		return nil, fmt.Errorf("%w: %s", apperrors.ErrGitHubUnsupportedBlobEncoding, out.Encoding)
	}

	// The API wraps base64 payloads at 60 columns.
	cleaned := strings.NewReplacer("\n", "", "\r", "").Replace(out.Content)

	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode blob %s: %w", blobSHA, err)
	}

	return data, nil
}

// createBlob uploads file content and returns its blob SHA.
func (a *githubAPI) createBlob(ctx context.Context, content []byte) (string, error) {
	body := struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{
		Content:  base64.StdEncoding.EncodeToString(content),
		Encoding: gitHubEncodingBase64,
	}

	var out gitHubBlobResponse
	if err := a.do(ctx, http.MethodPost, a.repoPath("/git/blobs"), body, &out); err != nil {
		return "", err
	}

	return out.SHA, nil
}

// createTree builds a new tree on top of baseTree. An empty baseTree creates a
// standalone tree, which is what an empty repository needs.
func (a *githubAPI) createTree(
	ctx context.Context, baseTree string, entries []gitHubTreeRequestEntry,
) (string, error) {
	body := struct {
		BaseTree string                   `json:"base_tree,omitempty"`
		Tree     []gitHubTreeRequestEntry `json:"tree"`
	}{
		BaseTree: baseTree,
		Tree:     entries,
	}

	var out gitHubTreeResponse
	if err := a.do(ctx, http.MethodPost, a.repoPath("/git/trees"), body, &out); err != nil {
		return "", err
	}

	return out.SHA, nil
}

// createCommit creates a commit object pointing at treeSHA.
func (a *githubAPI) createCommit(
	ctx context.Context, message, treeSHA string, parents []string, author gitHubAuthor,
) (string, error) {
	if parents == nil {
		parents = []string{}
	}

	body := struct {
		Message   string       `json:"message"`
		Tree      string       `json:"tree"`
		Parents   []string     `json:"parents"`
		Author    gitHubAuthor `json:"author"`
		Committer gitHubAuthor `json:"committer"`
	}{
		Message:   message,
		Tree:      treeSHA,
		Parents:   parents,
		Author:    author,
		Committer: author,
	}

	var out gitHubCommitResponse
	if err := a.do(ctx, http.MethodPost, a.repoPath("/git/commits"), body, &out); err != nil {
		return "", err
	}

	return out.SHA, nil
}

// updateRef fast-forwards a branch to commitSHA.
//
// force is always false: forcing would silently discard a concurrent writer's
// commit. A rejected update is reported so the caller can rebuild and retry.
func (a *githubAPI) updateRef(ctx context.Context, branch, commitSHA string) error {
	body := struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}{
		SHA:   commitSHA,
		Force: false,
	}

	path := a.repoPath("/git/refs/heads/" + url.PathEscape(branch))

	err := a.do(ctx, http.MethodPatch, path, body, nil)
	if err == nil {
		return nil
	}

	if isGitHubStatus(err, http.StatusUnprocessableEntity) || isGitHubStatus(err, http.StatusConflict) {
		return fmt.Errorf("%w: %w", apperrors.ErrGitHubRefMoved, err)
	}

	return err
}

// createRef creates a branch pointing at commitSHA.
func (a *githubAPI) createRef(ctx context.Context, branch, commitSHA string) error {
	body := struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{
		Ref: "refs/heads/" + branch,
		SHA: commitSHA,
	}

	err := a.do(ctx, http.MethodPost, a.repoPath("/git/refs"), body, nil)
	if err == nil {
		return nil
	}

	if isGitHubStatus(err, http.StatusUnprocessableEntity) {
		return fmt.Errorf("%w: %w", apperrors.ErrGitHubRefMoved, err)
	}

	return err
}
