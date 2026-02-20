// Package cmd provides the CLI commands for notion-sync.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/queue"
	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/sync"
	"github.com/fclairamb/ntnsync/internal/version"
	"github.com/fclairamb/ntnsync/internal/webhook"
)

const (
	// Default ports.
	defaultWebhookPort = 8080
)

var (
	// konfig is the global koanf instance.
	konfig = koanf.New(".")
)

// verboseFlag is the shared verbose flag for all commands.
var verboseFlag = &cli.BoolFlag{
	Name:  "verbose",
	Usage: "Enable verbose logging",
}

// LogFormat represents the log output format.
type LogFormat string

const (
	// LogFormatText is the human-readable text format (default).
	LogFormatText LogFormat = "text"
	// LogFormatJSON is the JSON-formatted structured logs.
	LogFormatJSON LogFormat = "json"
)

// getLogFormat returns the configured log format from NTN_LOG_FORMAT environment variable.
func getLogFormat() LogFormat {
	val := strings.ToLower(os.Getenv("NTN_LOG_FORMAT"))
	switch val {
	case "json":
		return LogFormatJSON
	case "text", "":
		return LogFormatText
	default:
		// Invalid format - will warn after logger is set up
		return LogFormatText
	}
}

// setupLogging configures the global logger based on the verbose flag and NTN_LOG_FORMAT.
func setupLogging(cmd *cli.Command) {
	level := slog.LevelInfo
	if cmd.Bool("verbose") {
		level = slog.LevelDebug
	}

	format := getLogFormat()
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch format {
	case LogFormatJSON:
		handler = slog.NewJSONHandler(os.Stderr, opts)
	case LogFormatText:
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))

	// Warn about invalid format after logger is set up
	envVal := strings.ToLower(os.Getenv("NTN_LOG_FORMAT"))
	if envVal != "" && envVal != "text" && envVal != "json" {
		slog.Warn("Invalid NTN_LOG_FORMAT value, using text format", "value", envVal)
	}

	if level == slog.LevelDebug {
		slog.Debug("Verbose logging enabled")
	}

	// Display storage mode
	cfg := store.LoadRemoteConfigFromEnv()
	mode := cfg.EffectiveStorageMode()
	storePath := resolveStorePath(cmd)
	if mode == store.StorageModeRemote {
		slog.Info("storage mode", "mode", "remote", "url", cfg.URL, "dir", storePath)
	} else {
		slog.Info("storage mode", "mode", "local", "dir", storePath)
	}
}

// NewApp creates the CLI application.
func NewApp() *cli.Command {
	return &cli.Command{
		Name:    "notion-sync",
		Usage:   "Synchronize Notion content to a git repository using folder-based organization",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "token",
				Usage:   "Notion API token",
				Sources: cli.EnvVars("NOTION_TOKEN"),
			},
			&cli.StringFlag{
				Name:    "store-path",
				Usage:   "Path to the git repository",
				Aliases: []string{"s"},
				Value:   "notion",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, _ *cli.Command) (context.Context, error) {
			// Load environment variables with NTN_ prefix
			if err := konfig.Load(env.Provider(".", env.Opt{
				Prefix: "NTN_",
			}), nil); err != nil {
				return ctx, fmt.Errorf("load env: %w", err)
			}

			return ctx, nil
		},
		Commands: []*cli.Command{
			getCommand(),
			scanCommand(),
			pullCommand(),
			syncCommand(),
			listCommand(),
			statusCommand(),
			cleanupCommand(),
			reindexCommand(),
			remoteCommand(),
			serveCommand(),
		},
	}
}

// getCommand creates the get subcommand.
func getCommand() *cli.Command {
	return &cli.Command{
		Name:      "get",
		Usage:     "Fetch a single page and place it in the hierarchy based on its parents",
		ArgsUsage: "<page_id_or_url>",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "folder",
				Aliases: []string{"f"},
				Usage:   "Folder name (optional, auto-detected from parent chain)",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Get page ID or URL from args
			if cmd.Args().Len() < 1 {
				return apperrors.ErrPageIDRequired
			}

			pageInput := cmd.Args().Get(0)
			folder := cmd.String("folder")

			// Parse page ID from URL or raw ID
			pageID, err := notion.ParsePageIDOrURL(pageInput)
			if err != nil {
				return fmt.Errorf("invalid page ID or URL: %w", err)
			}

			// Setup client and store
			client, store, err := setupClientAndStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler
			crawler := sync.NewCrawler(client, store, sync.WithCrawlerLogger(slog.Default()))

			// Get the page
			if err := crawler.GetPage(ctx, pageID, folder); err != nil {
				return fmt.Errorf("get page: %w", err)
			}

			slog.Info("page retrieved successfully", "page_id", pageID)

			return nil
		},
	}
}

// scanCommand creates the scan subcommand.
func scanCommand() *cli.Command {
	return &cli.Command{
		Name:      "scan",
		Usage:     "Re-scan a page to discover and queue all child pages",
		ArgsUsage: "<page_id_or_url>",
		Flags: []cli.Flag{
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Get page ID or URL from args
			if cmd.Args().Len() < 1 {
				return apperrors.ErrPageIDRequired
			}

			pageInput := cmd.Args().Get(0)

			// Parse page ID from URL or raw ID
			pageID, err := notion.ParsePageIDOrURL(pageInput)
			if err != nil {
				return fmt.Errorf("invalid page ID or URL: %w", err)
			}

			// Setup client and store
			client, store, err := setupClientAndStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler
			crawler := sync.NewCrawler(client, store, sync.WithCrawlerLogger(slog.Default()))

			// Scan the page
			if err := crawler.ScanPage(ctx, pageID); err != nil {
				return fmt.Errorf("scan page: %w", err)
			}

			displayScanComplete()
			return nil
		},
	}
}

// pullCommand creates the pull subcommand.
func pullCommand() *cli.Command {
	return &cli.Command{
		Name:  "pull",
		Usage: "Fetch all pages changed since last pull and queue them for sync",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "folder",
				Aliases: []string{"f"},
				Usage:   "Only pull changes for pages in specified folder",
			},
			&cli.DurationFlag{
				Name:    "since",
				Aliases: []string{"s"},
				Usage:   "Duration override (e.g., 24h, 7d) - overrides stored timestamp",
			},
			&cli.IntFlag{
				Name:    "max-pages",
				Aliases: []string{"n"},
				Usage:   "Maximum number of pages to queue (0 = unlimited)",
			},
			&cli.BoolFlag{
				Name:  "all",
				Usage: "Include pages not yet tracked (discover new pages)",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Preview changes without modifying anything",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			folder := cmd.String("folder")
			since := cmd.Duration("since")
			maxPages := cmd.Int("max-pages")
			all := cmd.Bool("all")
			dryRun := cmd.Bool("dry-run")
			verbose := cmd.Bool("verbose")

			// Setup client and store
			client, store, err := setupClientAndStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler
			crawler := sync.NewCrawler(client, store, sync.WithCrawlerLogger(slog.Default()))

			// Reconcile root.md
			if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
				return fmt.Errorf("reconcile root.md: %w", reconcileErr)
			}

			// Execute pull
			result, err := crawler.Pull(ctx, sync.PullOptions{
				Folder:   folder,
				Since:    since,
				MaxPages: maxPages,
				All:      all,
				DryRun:   dryRun,
				Verbose:  verbose,
			})
			if err != nil {
				return fmt.Errorf("pull: %w", err)
			}

			// Display results
			displayPullResults(result, all, dryRun)

			return nil
		},
	}
}

// syncCommand creates the sync subcommand.
//
//nolint:funlen // CLI command with many flags
func syncCommand() *cli.Command {
	return &cli.Command{
		Name:  "sync",
		Usage: "Process the queue and sync all pages recursively",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "folder",
				Aliases: []string{"f"},
				Usage:   "Only sync pages in specified folder",
			},
			&cli.IntFlag{
				Name:    "max-pages",
				Aliases: []string{"n"},
				Usage:   "Maximum number of pages to fetch (0 = unlimited)",
				Value:   0,
			},
			&cli.IntFlag{
				Name:    "max-files",
				Aliases: []string{"w"},
				Usage:   "Maximum number of markdown files to write (0 = unlimited)",
				Value:   0,
			},
			&cli.DurationFlag{
				Name:    "max-time",
				Aliases: []string{"t", "stop-after"},
				Usage:   "Maximum time to spend syncing (e.g., 30s, 2m, 1h) (0 = unlimited)",
				Value:   0,
			},
			&cli.IntFlag{
				Name:    "max-queue-files",
				Aliases: []string{"q"},
				Usage:   "Maximum number of queue files to process (0 = unlimited)",
				Value:   0,
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			folder := cmd.String("folder")
			maxPages := cmd.Int("max-pages")
			maxFiles := cmd.Int("max-files")
			maxTime := cmd.Duration("max-time")
			maxQueueFiles := cmd.Int("max-queue-files")

			// Setup client and store
			client, storeInst, err := setupClientAndStore(cmd)
			if err != nil {
				return err
			}

			// Get remote config for commit/push settings
			remoteConfig := storeRemoteConfig(storeInst)

			// Pull from remote before processing (if remote is configured)
			if err = storePull(ctx, storeInst); err != nil {
				return fmt.Errorf("pull from remote: %w", err)
			}

			// Create crawler
			crawler := sync.NewCrawler(client, storeInst, sync.WithCrawlerLogger(slog.Default()))

			// Reconcile root.md
			if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
				return fmt.Errorf("reconcile root.md: %w", reconcileErr)
			}

			// Process queue with limits and periodic commit support
			commitPeriod := remoteConfig.GetCommitPeriod()
			if commitPeriod > 0 {
				// Use periodic commit callback
				tracker := newCommitTracker(commitPeriod)
				err = crawler.ProcessQueueWithCallback(ctx, folder, maxPages, maxFiles, maxQueueFiles, maxTime,
					func() error {
						if tracker.shouldCommit() {
							if commitErr := commitAndPush(ctx, crawler, storeInst, remoteConfig, "periodic sync"); commitErr != nil {
								return commitErr
							}
							tracker.markCommitted()
						}
						return nil
					})
			} else {
				err = crawler.ProcessQueue(ctx, folder, maxPages, maxFiles, maxQueueFiles, maxTime)
			}
			if err != nil {
				return fmt.Errorf("process queue: %w", err)
			}

			// Final commit if enabled (via NTN_COMMIT or NTN_COMMIT_PERIOD)
			if remoteConfig.IsCommitEnabled() {
				if commitErr := commitAndPush(ctx, crawler, storeInst, remoteConfig, "sync complete"); commitErr != nil {
					return commitErr
				}
			}

			slog.Info("sync complete")
			return nil
		},
	}
}

// listCommand creates the list subcommand.
func listCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List all folders and their pages",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "folder",
				Aliases: []string{"f"},
				Usage:   "Only list pages in specified folder",
			},
			&cli.BoolFlag{
				Name:    "tree",
				Aliases: []string{"t"},
				Usage:   "Display as tree structure",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			folder := cmd.String("folder")
			tree := cmd.Bool("tree")

			// Setup store (no client needed for listing)
			storeInst, _, err := createStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler (no client needed for list)
			crawler := sync.NewCrawler(nil, storeInst, sync.WithCrawlerLogger(slog.Default()))

			// Reconcile root.md
			if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
				return fmt.Errorf("reconcile root.md: %w", reconcileErr)
			}

			// Get page list
			folders, err := crawler.ListPages(ctx, folder, tree)
			if err != nil {
				return fmt.Errorf("list pages: %w", err)
			}

			if len(folders) == 0 {
				displayNoFoldersMessage()
				return nil
			}

			displayPageList(folders, tree)
			return nil
		},
	}
}

// statusCommand creates the status subcommand.
func statusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Show sync status and queue information",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "folder",
				Aliases: []string{"f"},
				Usage:   "Only show status for specified folder",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			folder := cmd.String("folder")

			// Setup store (no client needed for status)
			storeInst, _, err := createStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler (no client needed for status)
			crawler := sync.NewCrawler(nil, storeInst, sync.WithCrawlerLogger(slog.Default()))

			// Reconcile root.md
			if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
				return fmt.Errorf("reconcile root.md: %w", reconcileErr)
			}

			// Get status
			status, err := crawler.GetStatus(ctx, folder)
			if err != nil {
				return fmt.Errorf("get status: %w", err)
			}

			// Display status
			if folder != "" {
				displayFolderStatus(folder, status)
			} else {
				displayOverallStatus(status)
			}

			return nil
		},
	}
}

// reindexCommand creates the reindex subcommand.
func reindexCommand() *cli.Command {
	return &cli.Command{
		Name:  "reindex",
		Usage: "Rebuild registry from markdown files",
		Flags: []cli.Flag{
			verboseFlag,
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Show what would be done without making changes",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			storeInst, _, err := createStore(cmd)
			if err != nil {
				return err
			}

			crawler := sync.NewCrawler(nil, storeInst, sync.WithCrawlerLogger(slog.Default()))
			dryRun := cmd.Bool("dry-run")

			if err := crawler.Reindex(ctx, dryRun); err != nil {
				return fmt.Errorf("reindex: %w", err)
			}

			return nil
		},
	}
}

// cleanupCommand creates the cleanup subcommand.
func cleanupCommand() *cli.Command {
	return &cli.Command{
		Name:  "cleanup",
		Usage: "Delete orphaned pages not tracing to root.md",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Preview only, don't delete anything",
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			dryRun := cmd.Bool("dry-run")

			// Setup store (no client needed for cleanup)
			storeInst, remoteConfig, err := createStore(cmd)
			if err != nil {
				return err
			}

			// Create crawler (no client needed for cleanup)
			crawler := sync.NewCrawler(nil, storeInst, sync.WithCrawlerLogger(slog.Default()))

			// Reconcile root.md first
			if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
				return fmt.Errorf("reconcile root.md: %w", reconcileErr)
			}

			// Run cleanup
			result, err := crawler.Cleanup(ctx, dryRun)
			if err != nil {
				return fmt.Errorf("cleanup: %w", err)
			}

			// Display results
			displayCleanupResults(result, dryRun)

			// Commit if enabled and not dry-run
			if !dryRun && remoteConfig.IsCommitEnabled() && result.DeletedFiles > 0 {
				if err := commitAndPush(ctx, crawler, storeInst, remoteConfig, "cleanup orphaned pages"); err != nil {
					return err
				}
			}

			return nil
		},
	}
}

// remoteCommand creates the remote subcommand.
func remoteCommand() *cli.Command {
	return &cli.Command{
		Name:  "remote",
		Usage: "Manage remote git repository",
		Commands: []*cli.Command{
			{
				Name:  "show",
				Usage: "Show current remote configuration from environment variables",
				Flags: []cli.Flag{
					verboseFlag,
				},
				Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
					setupLogging(cmd)
					return ctx, nil
				},
				Action: func(_ context.Context, _ *cli.Command) error {
					cfg := store.LoadRemoteConfigFromEnv()
					displayRemoteConfig(cfg)
					return nil
				},
			},
			{
				Name:  "test",
				Usage: "Test connection to remote repository",
				Flags: []cli.Flag{
					verboseFlag,
				},
				Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
					setupLogging(cmd)
					return ctx, nil
				},
				Action: func(ctx context.Context, _ *cli.Command) error {
					cfg := store.LoadRemoteConfigFromEnv()

					if !cfg.IsEnabled() {
						return apperrors.ErrRemoteNotConfiguredSetURL
					}

					return displayConnectionTest(ctx, cfg)
				},
			},
		},
	}
}

// serveCommand creates the serve subcommand for the webhook server.
//
//nolint:funlen // CLI command with many flags
func serveCommand() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Start the webhook server to receive Notion events",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "port",
				Aliases: []string{"p"},
				Usage:   "HTTP port to listen on",
				Value:   defaultWebhookPort,
				Sources: cli.EnvVars("NTN_WEBHOOK_PORT"),
			},
			&cli.StringFlag{
				Name:    "secret",
				Usage:   "Webhook secret for signature verification (optional, skips verification if not set)",
				Sources: cli.EnvVars("NTN_WEBHOOK_SECRET"),
			},
			&cli.StringFlag{
				Name:    "path",
				Usage:   "Webhook endpoint path",
				Value:   "/webhooks/notion",
				Sources: cli.EnvVars("NTN_WEBHOOK_PATH"),
			},
			&cli.BoolFlag{
				Name:    "auto-sync",
				Usage:   "Automatically sync after queuing webhook events",
				Value:   true,
				Sources: cli.EnvVars("NTN_WEBHOOK_AUTO_SYNC"),
			},
			&cli.DurationFlag{
				Name:    "sync-delay",
				Usage:   "Delay before processing queue after webhook (debounce)",
				Value:   0,
				Sources: cli.EnvVars("NTN_WEBHOOK_SYNC_DELAY"),
			},
			verboseFlag,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			setupLogging(cmd)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			secret := cmd.String("secret")
			if secret == "" {
				slog.Warn("webhook secret not configured - signature verification disabled (set --secret or NTN_WEBHOOK_SECRET)")
			}

			// Setup store (webhook server needs it for queue management)
			storeInst, remoteConfig, err := createStore(cmd)
			if err != nil {
				return err
			}

			// Create queue manager
			queueMgr := queue.NewManager(storeInst, slog.Default())

			// Create webhook config
			cfg := &webhook.ServerConfig{
				Port:      cmd.Int("port"),
				Path:      cmd.String("path"),
				Secret:    secret,
				AutoSync:  cmd.Bool("auto-sync"),
				SyncDelay: cmd.Duration("sync-delay"),
			}

			// Create sync worker if NOTION_TOKEN is available
			var syncWorker *webhook.SyncWorker
			token := cmd.String("token")
			if token == "" {
				token = os.Getenv("NOTION_TOKEN")
			}

			if token != "" && cfg.AutoSync {
				client := notion.NewClient(token)
				crawler := sync.NewCrawler(client, storeInst, sync.WithCrawlerLogger(slog.Default()))

				// Reconcile root.md at startup
				if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
					return fmt.Errorf("reconcile root.md: %w", reconcileErr)
				}

				opts := []webhook.SyncWorkerOption{}
				if cfg.SyncDelay > 0 {
					opts = append(opts, webhook.WithSyncDelay(cfg.SyncDelay))
				}

				syncWorker = webhook.NewSyncWorker(crawler, storeInst, remoteConfig, slog.Default(), opts...)
				slog.Info("auto-sync enabled", "sync_delay", cfg.SyncDelay)
			} else if cfg.AutoSync {
				slog.Warn("auto-sync disabled: NOTION_TOKEN not configured")
			}

			// Create and start server
			server := webhook.NewServer(cfg, queueMgr, storeInst, slog.Default(), syncWorker, remoteConfig)

			slog.Info("starting webhook server",
				"port", cfg.Port,
				"path", cfg.Path,
				"auto_sync", cfg.AutoSync,
				"sync_delay", cfg.SyncDelay,
				"version", version.Version)

			return server.Start(ctx)
		},
	}
}

// storeRemoteConfig returns the remote config from a store, supporting both LocalStore and SplitStore.
func storeRemoteConfig(storeInst store.Store) *store.RemoteConfig {
	switch typed := storeInst.(type) {
	case *store.LocalStore:
		return typed.RemoteConfig()
	case *store.SplitStore:
		return typed.RemoteConfig()
	default:
		return nil
	}
}

// storePull pulls from remote if the store supports it.
func storePull(ctx context.Context, storeInst store.Store) error {
	switch typed := storeInst.(type) {
	case *store.LocalStore:
		if typed.IsRemoteEnabled() {
			return typed.Pull(ctx)
		}
	case *store.SplitStore:
		if typed.IsRemoteEnabled() {
			return typed.Pull(ctx)
		}
	}
	return nil
}

// resolveStorePath returns the store path from NTN_DIR env var or --store-path flag.
func resolveStorePath(cmd *cli.Command) string {
	// NTN_DIR env var takes precedence
	if ntnDir := os.Getenv("NTN_DIR"); ntnDir != "" {
		return ntnDir
	}

	storePath := cmd.String("store-path")
	if storePath == "" {
		storePath = "notion"
	}
	return storePath
}

// createStore creates a store from command flags.
// If NTN_METADATA_BRANCH is set, returns a SplitStore that routes metadata
// to a separate branch. Otherwise, returns a plain LocalStore.
func createStore(cmd *cli.Command) (store.Store, *store.RemoteConfig, error) {
	storePath := resolveStorePath(cmd)
	remoteConfig := store.LoadRemoteConfigFromEnv()

	contentStore, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
	if err != nil {
		return nil, nil, fmt.Errorf("create store: %w", err)
	}

	if remoteConfig.HasMetadataBranch() {
		metadataPath := filepath.Join(storePath, ".notion-sync-repo")

		metadataRemoteConfig := &store.RemoteConfig{
			Storage:      remoteConfig.Storage,
			URL:          remoteConfig.URL,
			Password:     remoteConfig.Password,
			Branch:       remoteConfig.MetadataBranch,
			User:         remoteConfig.User,
			Email:        remoteConfig.Email,
			Commit:       remoteConfig.Commit,
			CommitPeriod: remoteConfig.CommitPeriod,
			Push:         remoteConfig.Push,
		}

		metadataStore, err := store.NewLocalStore(metadataPath,
			store.WithRemoteConfig(metadataRemoteConfig),
			store.WithLogger(slog.Default()))
		if err != nil {
			return nil, nil, fmt.Errorf("create metadata store: %w", err)
		}

		slog.Info("metadata branch enabled",
			"branch", remoteConfig.MetadataBranch,
			"path", metadataPath)

		return store.NewSplitStore(contentStore, metadataStore), remoteConfig, nil
	}

	return contentStore, remoteConfig, nil
}

// setupClientAndStore creates the Notion client and store from command flags.
func setupClientAndStore(cmd *cli.Command) (*notion.Client, store.Store, error) {
	token := cmd.String("token")
	if token == "" {
		token = os.Getenv("NOTION_TOKEN")
	}
	if token == "" {
		return nil, nil, apperrors.ErrNotionTokenRequired
	}

	storeInst, _, err := createStore(cmd)
	if err != nil {
		return nil, nil, err
	}

	client := notion.NewClient(token)
	return client, storeInst, nil
}
