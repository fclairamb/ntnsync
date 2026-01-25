package webhook

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/sync"
)

// mockCrawler is a mock implementation of sync.Crawler for testing.
type mockCrawler struct {
	processCount atomic.Int32
	processDelay time.Duration
}

func (m *mockCrawler) ProcessQueue(ctx context.Context, _ string, _ int, _ int, _ int, _ time.Duration) error {
	m.processCount.Add(1)
	if m.processDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.processDelay):
		}
	}
	return nil
}

func (m *mockCrawler) ProcessQueueWithCallback(ctx context.Context, folderFilter string, maxPages int, maxFiles int, maxQueueFiles int, maxTime time.Duration, _ sync.QueueCallback) error {
	return m.ProcessQueue(ctx, folderFilter, maxPages, maxFiles, maxQueueFiles, maxTime)
}

func (m *mockCrawler) CommitChanges(_ context.Context, _ string) error {
	return nil
}

// createTestWorker creates a SyncWorker for testing.
// Tests are simplified since we don't need actual sync functionality.
func createTestWorker(t *testing.T, opts ...SyncWorkerOption) *SyncWorker {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create worker with minimal setup for notification testing
	worker := &SyncWorker{
		crawler:      nil, // Not used in notification tests
		localStore:   nil, // Not used in notification tests
		remoteConfig: nil, // No commits in tests
		logger:       logger,
		notify:       make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(worker)
	}

	return worker
}

// TestSyncWorker_NotifyNonBlocking verifies that Notify is non-blocking.
func TestSyncWorker_NotifyNonBlocking(t *testing.T) {
	t.Parallel()
	worker := createTestWorker(t)

	// Multiple rapid notifications should not block
	done := make(chan struct{})
	go func() {
		for range 100 {
			worker.Notify()
		}
		close(done)
	}()

	select {
	case <-done:
		// Success - notifications completed without blocking
	case <-time.After(1 * time.Second):
		t.Fatal("Notify blocked when it should be non-blocking")
	}
}

// TestSyncWorker_ProcessOnNotify verifies that the worker processes the queue when notified.
func TestSyncWorker_ProcessOnNotify(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping - requires full crawler implementation")
	crawler := &mockCrawler{}
	worker := createTestWorker(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker in background
	workerDone := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(workerDone)
	}()

	// Send notification
	worker.Notify()

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	if crawler.processCount.Load() != 1 {
		t.Errorf("expected 1 process call, got %d", crawler.processCount.Load())
	}

	// Cancel and wait for worker to stop
	cancel()
	select {
	case <-workerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

// TestSyncWorker_GracefulCancellation verifies that the worker stops when context is canceled.
func TestSyncWorker_GracefulCancellation(t *testing.T) {
	t.Parallel()
	worker := createTestWorker(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Start worker
	workerDone := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(workerDone)
	}()

	// Give worker time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel
	cancel()

	// Verify worker stops
	select {
	case <-workerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not stop gracefully")
	}
}

// TestSyncWorker_SyncDelay verifies that the sync delay is respected.
func TestSyncWorker_SyncDelay(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping - requires full crawler implementation")
	crawler := &mockCrawler{}

	delay := 100 * time.Millisecond
	worker := createTestWorker(t, WithSyncDelay(delay))

	ctx := t.Context()

	// Start worker
	go worker.Start(ctx)

	// Notify
	startTime := time.Now()
	worker.Notify()

	// Wait a bit less than the delay - should not have processed yet
	time.Sleep(50 * time.Millisecond)
	if crawler.processCount.Load() != 0 {
		t.Error("expected no processing before delay elapsed")
	}

	// Wait for delay to pass
	time.Sleep(100 * time.Millisecond)
	elapsed := time.Since(startTime)

	if crawler.processCount.Load() != 1 {
		t.Errorf("expected 1 process call after delay, got %d", crawler.processCount.Load())
	}

	if elapsed < delay {
		t.Errorf("processing started before delay: elapsed %v, delay %v", elapsed, delay)
	}
}

// TestSyncWorker_CoalesceNotifications verifies that multiple rapid notifications coalesce.
func TestSyncWorker_CoalesceNotifications(t *testing.T) {
	t.Parallel()
	t.Skip("Skipping - requires full crawler implementation")
	// Crawler with a small delay to simulate work
	crawler := &mockCrawler{processDelay: 50 * time.Millisecond}
	worker := createTestWorker(t)

	ctx := t.Context()

	// Start worker
	go worker.Start(ctx)

	// Send multiple notifications rapidly
	for range 10 {
		worker.Notify()
	}

	// Wait for processing to complete
	time.Sleep(300 * time.Millisecond)

	// Should have processed at most 2 times (one in progress, one queued)
	count := crawler.processCount.Load()
	if count > 2 {
		t.Errorf("expected at most 2 process calls due to coalescing, got %d", count)
	}
}
