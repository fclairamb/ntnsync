package webhook

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/sync"
)

// SyncWorker processes queued items in the background.
type SyncWorker struct {
	crawler      *sync.Crawler
	store        store.Store
	remoteConfig *store.RemoteConfig
	logger       *slog.Logger
	syncDelay    time.Duration
	notify       chan struct{}
}

// SyncWorkerOption configures the SyncWorker.
type SyncWorkerOption func(*SyncWorker)

// WithSyncDelay sets the debounce delay before processing.
// This allows multiple rapid notifications to coalesce into a single sync.
func WithSyncDelay(d time.Duration) SyncWorkerOption {
	return func(w *SyncWorker) {
		w.syncDelay = d
	}
}

// NewSyncWorker creates a new sync worker.
func NewSyncWorker(
	crawler *sync.Crawler,
	st store.Store,
	remoteConfig *store.RemoteConfig,
	logger *slog.Logger,
	opts ...SyncWorkerOption,
) *SyncWorker {
	worker := &SyncWorker{
		crawler:      crawler,
		store:        st,
		remoteConfig: remoteConfig,
		logger:       logger,
		notify:       make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(worker)
	}

	return worker
}

// Notify signals that there is new work to process.
// This is non-blocking - if a notification is already pending, it's a no-op.
func (w *SyncWorker) Notify() {
	select {
	case w.notify <- struct{}{}:
		w.logger.Debug("sync worker notified")
	default:
		w.logger.Debug("sync worker notification skipped (already pending)")
	}
}

// Start runs the sync worker until the context is canceled or a fatal error occurs.
// This method blocks and should be called in a goroutine.
func (w *SyncWorker) Start(ctx context.Context) {
	w.logger.InfoContext(ctx, "sync worker started", "sync_delay", w.syncDelay)

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "sync worker stopping")
			return
		case <-w.notify:
			if err := w.processWithDelay(ctx); err != nil {
				w.logger.ErrorContext(ctx, "sync worker encountered fatal error, exiting process", "error", err)
				os.Exit(1)
			}
		}
	}
}

// processWithDelay waits for the sync delay (if configured) then processes the queue.
func (w *SyncWorker) processWithDelay(ctx context.Context) error {
	if w.syncDelay > 0 {
		w.logger.DebugContext(ctx, "waiting for sync delay", "delay", w.syncDelay)

		timer := time.NewTimer(w.syncDelay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			// Continue to process
		}
	}

	return w.processQueue(ctx)
}

// processQueue processes all queued items with periodic commits.
func (w *SyncWorker) processQueue(ctx context.Context) error {
	w.logger.InfoContext(ctx, "sync worker processing queue")

	var err error
	commitPeriod := w.remoteConfig.GetCommitPeriod()

	if commitPeriod > 0 {
		// Use periodic commit callback
		tracker := newCommitTracker(commitPeriod)
		err = w.crawler.ProcessQueueWithCallback(ctx, "", 0, 0, 0, 0,
			func() error {
				if tracker.shouldCommit() {
					if commitErr := w.commitAndPush(ctx, "periodic sync"); commitErr != nil {
						return commitErr
					}
					tracker.markCommitted()
				}
				return nil
			})
	} else {
		err = w.crawler.ProcessQueue(ctx, "", 0, 0, 0, 0)
	}

	if err != nil {
		w.logger.ErrorContext(ctx, "sync worker failed to process queue", "error", err)
		return fmt.Errorf("process queue: %w", err)
	}

	// Final commit if enabled
	if w.remoteConfig != nil && w.remoteConfig.IsCommitEnabled() {
		if err := w.commitAndPush(ctx, "sync complete"); err != nil {
			w.logger.ErrorContext(ctx, "failed to commit after sync", "error", err)
			return fmt.Errorf("final commit: %w", err)
		}
	}

	w.logger.InfoContext(ctx, "sync worker completed queue processing")
	return nil
}

// commitAndPush commits changes and optionally pushes to remote.
func (w *SyncWorker) commitAndPush(ctx context.Context, reason string) error {
	message := fmt.Sprintf("[ntnsync] %s at %s", reason, time.Now().Format(time.RFC3339))
	if err := w.crawler.CommitChanges(ctx, message); err != nil {
		w.logger.WarnContext(ctx, "failed to commit changes", "error", err, "reason", reason)
		return nil // Don't fail the sync for commit errors
	}

	// Push if enabled
	if w.remoteConfig.IsPushEnabled() {
		if err := w.pushWithRetry(ctx); err != nil {
			return fmt.Errorf("push to remote: %w", err)
		}
	}

	return nil
}

// pushWithRetry attempts to push to remote with exponential backoff retry logic.
func (w *SyncWorker) pushWithRetry(ctx context.Context) error {
	const (
		maxRetries    = 3
		initialDelay  = 5 * time.Second
		backoffFactor = 2.0
	)

	var lastErr error
	delay := initialDelay

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			w.logger.InfoContext(ctx, "retrying push after delay",
				"attempt", attempt,
				"max_attempts", maxRetries,
				"delay", delay,
				"previous_error", lastErr)

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				// Continue to retry
			}
		}

		if err := w.store.Push(ctx); err != nil {
			lastErr = err
			w.logger.WarnContext(ctx, "push failed",
				"attempt", attempt+1,
				"max_attempts", maxRetries+1,
				"error", err)

			// If this wasn't the last attempt, prepare for next retry
			if attempt < maxRetries {
				delay = time.Duration(float64(delay) * backoffFactor)
			}
			continue
		}

		// Success
		if attempt > 0 {
			w.logger.InfoContext(ctx, "push succeeded after retry", "attempt", attempt+1)
		}
		return nil
	}

	return fmt.Errorf("push failed after %d attempts: %w", maxRetries+1, lastErr)
}

// commitTracker tracks time since last commit for periodic commits.
type commitTracker struct {
	lastCommit time.Time
	period     time.Duration
}

// newCommitTracker creates a new commit tracker with the given period.
func newCommitTracker(period time.Duration) *commitTracker {
	return &commitTracker{
		lastCommit: time.Now(),
		period:     period,
	}
}

// shouldCommit returns true if enough time has passed since last commit.
func (t *commitTracker) shouldCommit() bool {
	if t.period == 0 {
		return false
	}
	return time.Since(t.lastCommit) >= t.period
}

// markCommitted records that a commit was just made.
func (t *commitTracker) markCommitted() {
	t.lastCommit = time.Now()
}
