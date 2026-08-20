package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// sleepRecorder captures the durations the API client waits for.
type sleepRecorder struct {
	mu     sync.Mutex
	waits  []time.Duration
	failOn error
}

// sleep is the injected sleepFunc.
func (r *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.waits = append(r.waits, d)

	return r.failOn
}

// total returns the recorded waits.
func (r *sleepRecorder) total() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]time.Duration(nil), r.waits...)
}

// fixedNow is a deterministic clock for rate-limit arithmetic.
func fixedNow() time.Time {
	return time.Unix(1_000_000_000, 0)
}

// apiWithFake builds an API client wired to the fake, with recorded sleeps.
func apiWithFake(fake *fakeGitHub, recorder *sleepRecorder) *githubAPI {
	return newGitHubAPI("acme", "wiki", "token",
		withGitHubTransport(fake),
		withGitHubBaseURL("https://api.github.test"),
		withGitHubSleep(recorder.sleep),
		withGitHubClock(fixedNow),
	)
}

func TestGitHubAPIBlocksWhenBudgetFallsBelowFloor(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})

	// The window resets ten minutes from the fake clock's "now".
	fake.rateReset = fixedNow().Add(10 * time.Minute)
	fake.rateRemaining = gitHubRateFloor - 1

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	ctx := context.Background()

	// First call observes the exhausted budget without waiting.
	if _, _, err := api.getRef(ctx, "main"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if waits := recorder.total(); len(waits) != 0 {
		t.Fatalf("the first call must not wait, got %v", waits)
	}

	// The next call must block until the window resets.
	if _, _, err := api.getRef(ctx, "main"); err != nil {
		t.Fatalf("second call: %v", err)
	}

	waits := recorder.total()
	if len(waits) != 1 {
		t.Fatalf("expected exactly one wait, got %v", waits)
	}
	if waits[0] != 10*time.Minute {
		t.Fatalf("expected to wait until the reset (10m), got %v", waits[0])
	}
}

func TestGitHubAPIDoesNotBlockWhenBudgetIsHealthy(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})
	fake.rateReset = fixedNow().Add(10 * time.Minute)

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	ctx := context.Background()
	for range 3 {
		if _, _, err := api.getRef(ctx, "main"); err != nil {
			t.Fatalf("getRef: %v", err)
		}
	}

	if waits := recorder.total(); len(waits) != 0 {
		t.Fatalf("a healthy budget must never block, got %v", waits)
	}
}

func TestGitHubAPIRecordsRateBudget(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})
	fake.rateRemaining = 4242
	fake.rateLimit = 5000
	fake.rateReset = fixedNow().Add(time.Hour)

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	if _, _, err := api.getRef(context.Background(), "main"); err != nil {
		t.Fatalf("getRef: %v", err)
	}

	budget := api.rateSnapshot()
	if !budget.Known {
		t.Fatal("rate budget must be recorded from the response headers")
	}
	if budget.Remaining != 4242 || budget.Limit != 5000 {
		t.Fatalf("unexpected budget %+v", budget)
	}
	if !budget.Reset.Equal(fixedNow().Add(time.Hour)) {
		t.Fatalf("unexpected reset %v", budget.Reset)
	}
}

func TestGitHubAPIRetriesRateLimitedResponses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		headers map[string]string
	}{
		{
			name:   "403 with exhausted budget",
			status: http.StatusForbidden,
			headers: map[string]string{
				"X-Ratelimit-Remaining": "0",
				"X-Ratelimit-Reset":     strconv.FormatInt(fixedNow().Add(2*time.Minute).Unix(), 10),
			},
		},
		{
			name:   "429 too many requests",
			status: http.StatusTooManyRequests,
			headers: map[string]string{
				"X-Ratelimit-Remaining": "0",
				"X-Ratelimit-Reset":     strconv.FormatInt(fixedNow().Add(2*time.Minute).Unix(), 10),
			},
		},
		{
			name:    "500 server error",
			status:  http.StatusInternalServerError,
			headers: map[string]string{},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeGitHub()
			fake.seedFiles("main", map[string]string{"root.md": "# Root"})
			fake.rateReset = fixedNow().Add(time.Hour)

			fake.script("GET /git/ref/heads/main", scriptedResponse{
				status:  testCase.status,
				body:    `{"message":"rate limited"}`,
				headers: testCase.headers,
			})

			recorder := &sleepRecorder{}
			api := apiWithFake(fake, recorder)

			sha, found, err := api.getRef(context.Background(), "main")
			if err != nil {
				t.Fatalf("the call must succeed after a retry: %v", err)
			}
			if !found || sha == "" {
				t.Fatal("expected the retried call to resolve the ref")
			}

			waits := recorder.total()
			if len(waits) != 1 {
				t.Fatalf("expected exactly one backoff, got %v", waits)
			}
			if testCase.status != http.StatusInternalServerError && waits[0] != 2*time.Minute {
				t.Fatalf("a rate-limited response must wait for the reset, got %v", waits[0])
			}
		})
	}
}

func TestGitHubAPIDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})

	fake.script("GET /git/ref/heads/main", scriptedResponse{
		status: http.StatusUnauthorized,
		body:   `{"message":"Bad credentials"}`,
	})

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	if _, _, err := api.getRef(context.Background(), "main"); err == nil {
		t.Fatal("a 401 must be reported, not retried away")
	}

	if got := len(requestsOfKind(fake, http.MethodGet, "/git/ref/heads/")); got != 1 {
		t.Fatalf("a 401 must not be retried, got %d attempts", got)
	}
}

func TestGitHubAPIRejectsTruncatedTrees(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})

	fake.script("GET /git/trees/", scriptedResponse{
		status: http.StatusOK,
		body:   `{"sha":"deadbeef","truncated":true,"tree":[]}`,
	})

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	_, err := api.getTree(context.Background(), "deadbeef")
	if !errors.Is(err, apperrors.ErrGitHubTreeTruncated) {
		t.Fatalf("a truncated listing must be an error, not silently short: %v", err)
	}
}

func TestGitHubAPINeverSendsRecursiveQuery(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	head := fake.seedFiles("main", map[string]string{"tech/a.md": "A"})

	recorder := &sleepRecorder{}
	api := apiWithFake(fake, recorder)

	ctx := context.Background()

	treeSHA, err := api.getCommitTree(ctx, head)
	if err != nil {
		t.Fatalf("get commit tree: %v", err)
	}
	if _, err := api.getTree(ctx, treeSHA); err != nil {
		t.Fatalf("get tree: %v", err)
	}

	for _, req := range fake.recorded() {
		if req.RawQuery != "" {
			t.Fatalf("tree fetches must be plain, got query %q on %s", req.RawQuery, req.Path)
		}
	}
}

func TestGitHubAPISendsAuthAndVersionHeaders(t *testing.T) {
	t.Parallel()

	captured := &headerCapturingTransport{inner: newFakeGitHub()}
	captured.inner.seedFiles("main", map[string]string{"root.md": "# Root"})

	api := newGitHubAPI("acme", "wiki", "secret-token",
		withGitHubTransport(captured),
		withGitHubBaseURL("https://api.github.test"),
		withGitHubSleep(func(context.Context, time.Duration) error { return nil }),
	)

	if _, _, err := api.getRef(context.Background(), "main"); err != nil {
		t.Fatalf("getRef: %v", err)
	}

	if got := captured.header.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("unexpected Authorization header %q", got)
	}
	if got := captured.header.Get("X-Github-Api-Version"); got != gitHubAPIVersion {
		t.Fatalf("unexpected API version header %q", got)
	}
	if got := captured.header.Get("Accept"); got != gitHubAcceptJSON {
		t.Fatalf("unexpected Accept header %q", got)
	}
}

// headerCapturingTransport records the headers of the last request.
type headerCapturingTransport struct {
	inner  *fakeGitHub
	header http.Header
}

// RoundTrip implements http.RoundTripper.
func (c *headerCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.header = req.Header.Clone()
	return c.inner.RoundTrip(req)
}

func TestLRUCacheEvictsOldestEntries(t *testing.T) {
	t.Parallel()

	cache := newLRUCache[string](2)

	cache.Put("a", "1")
	cache.Put("b", "2")

	if _, ok := cache.Get("a"); !ok {
		t.Fatal("a must still be cached")
	}

	cache.Put("c", "3")

	if cache.Len() != 2 {
		t.Fatalf("cache must stay bounded, got %d entries", cache.Len())
	}
	if _, ok := cache.Get("b"); ok {
		t.Fatal("b was least recently used and must have been evicted")
	}
	if value, ok := cache.Get("a"); !ok || value != "1" {
		t.Fatalf("a must survive as the most recently used entry, got %q %v", value, ok)
	}
}
