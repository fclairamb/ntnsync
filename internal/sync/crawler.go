package sync

import (
	"context"
	"log/slog"

	"github.com/fclairamb/ntnsync/internal/converter"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
)

const (
	stateDir  = ".notion-sync"
	stateFile = "state.json"
	idsDir    = "ids"

	queueTypeInit       = "init"
	parentTypeBlockID   = "block_id"
	parentTypeWorkspace = "workspace"

	defaultUntitledStr = "untitled"

	// Filename conflict resolution.
	shortIDLength = 4 // Number of characters to use from page ID in conflict resolution

	// Frontmatter parsing.
	frontmatterFieldCount = 2 // Expected number of parts when splitting a frontmatter line on ":"

	// URL path parsing.
	minFileURLSegments = 2 // Minimum number of path segments in a Notion file URL
)

// Crawler synchronizes Notion pages to local storage using folder-based organization.
type Crawler struct {
	client       *notion.Client
	store        store.Store
	tx           store.Transaction
	state        *State
	queueManager *queue.Manager
	converter    *converter.Converter
	logger       *slog.Logger
}

// CrawlerOption configures the crawler.
type CrawlerOption func(*Crawler)

// WithCrawlerLogger sets a custom logger.
func WithCrawlerLogger(l *slog.Logger) CrawlerOption {
	return func(c *Crawler) {
		c.logger = l
	}
}

// NewCrawler creates a new crawler.
func NewCrawler(client *notion.Client, st store.Store, opts ...CrawlerOption) *Crawler {
	crawler := &Crawler{
		client:       client,
		store:        st,
		state:        NewState(),
		queueManager: queue.NewManager(st, slog.Default()),
		converter:    converter.NewConverter(),
		logger:       slog.Default(),
	}

	for _, opt := range opts {
		opt(crawler)
	}

	crawler.queueManager.Logger = crawler.logger

	return crawler
}

// EnsureTransaction ensures a transaction is available.
// If no transaction exists, creates a new one.
func (c *Crawler) EnsureTransaction(ctx context.Context) error {
	if c.tx != nil {
		return nil
	}
	tx, err := c.store.BeginTx(ctx)
	if err != nil {
		return err
	}
	c.tx = tx
	c.queueManager.SetTransaction(tx)
	return nil
}

// SetTransaction sets an external transaction.
func (c *Crawler) SetTransaction(tx store.Transaction) {
	c.tx = tx
	c.queueManager.SetTransaction(tx)
}

// Commit commits the current transaction with the given message.
// After commit, a new transaction is automatically started.
func (c *Crawler) Commit(_ context.Context, message string) error {
	if c.tx == nil {
		return nil
	}
	if err := c.tx.Commit(message); err != nil {
		return err
	}
	return nil
}

// Transaction returns the current transaction.
func (c *Crawler) Transaction() store.Transaction {
	return c.tx
}
