package webhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/version"
)

const (
	// HTTP server timeouts.
	readHeaderTimeout = 10 * time.Second // Timeout for reading request headers
	shutdownTimeout   = 30 * time.Second // Timeout for graceful shutdown
)

// Server represents the webhook HTTP server.
type Server struct {
	handler        *Handler
	httpServer     *http.Server
	config         *ServerConfig
	logger         *slog.Logger
	syncWorker     *SyncWorker
	syncWorkerDone chan struct{}
	cancelFunc     context.CancelFunc
}

// NewServer creates a new webhook server.
// If syncWorker is not nil, it will be started alongside the HTTP server.
func NewServer(
	cfg *ServerConfig,
	queueManager *queue.Manager,
	storeInst store.Store,
	logger *slog.Logger,
	syncWorker *SyncWorker,
	remoteConfig *store.RemoteConfig,
) *Server {
	handler := NewHandler(queueManager, storeInst, cfg.Secret, cfg.AutoSync, logger, syncWorker, remoteConfig)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handler.HandleHealth)
	mux.HandleFunc("/api/version", handler.HandleVersion)
	mux.HandleFunc(cfg.Path, handler.HandleWebhook)

	// Wrap with logging middleware
	loggedHandler := loggingMiddleware(mux, logger)

	return &Server{
		handler:    handler,
		config:     cfg,
		logger:     logger,
		syncWorker: syncWorker,
		httpServer: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Port),
			Handler:           loggedHandler,
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}
}

// Start starts the HTTP server. This method blocks until the server is stopped.
func (s *Server) Start(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting webhook server",
		"port", s.config.Port,
		"path", s.config.Path,
		"auto_sync", s.config.AutoSync,
		"sync_delay", s.config.SyncDelay,
		"version", version.Version,
		"commit", version.Commit,
		"build_time", version.GitTime)

	// Create a cancellable context for the sync worker
	workerCtx, cancel := context.WithCancel(ctx)
	s.cancelFunc = cancel

	// Start sync worker if configured
	if s.syncWorker != nil {
		s.syncWorkerDone = make(chan struct{})
		go func() {
			defer close(s.syncWorkerDone)
			s.syncWorker.Start(workerCtx)
		}()
		s.logger.InfoContext(ctx, "sync worker started")

		// Trigger initial processing of any existing queued items
		s.syncWorker.Notify()
	}

	// Start server in a goroutine so we can handle context cancellation
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		s.logger.InfoContext(ctx, "shutting down webhook server")
		// Use a new timeout context for shutdown to avoid canceled context issues
		// We create a detached context from the original context to allow shutdown to complete
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return s.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Cancel the sync worker context
	if s.cancelFunc != nil {
		s.cancelFunc()
	}

	// Wait for sync worker to finish
	if s.syncWorkerDone != nil {
		s.logger.InfoContext(ctx, "waiting for sync worker to finish")
		<-s.syncWorkerDone
		s.logger.InfoContext(ctx, "sync worker finished")
	}

	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's address. Useful for testing.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// loggingMiddleware logs all HTTP requests.
func loggingMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		// Wrap the response writer to capture status code
		wrapped := &responseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
			written:        false,
		}

		// Log incoming request
		logger.Info("http request",
			"method", req.Method,
			"path", req.URL.Path,
			"remote_addr", req.RemoteAddr,
			"user_agent", req.UserAgent())

		// Call the next handler
		next.ServeHTTP(wrapped, req)

		// Log response
		logger.Info("http response",
			"method", req.Method,
			"path", req.URL.Path,
			"status", wrapped.statusCode,
			"duration_ms", time.Since(start).Milliseconds())
	})
}
