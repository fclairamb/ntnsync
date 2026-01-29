// Package cmd provides the CLI commands for notion-sync.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

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
	// Time duration constants for relative time formatting.
	hoursPerDay  = 24
	daysPerWeek  = 7
	daysPerMonth = 30

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

			// Get local store for remote operations
			localStore, ok := storeInst.(*store.LocalStore)
			if !ok {
				return apperrors.ErrNotLocalStore
			}

			// Get remote config for commit/push settings
			remoteConfig := localStore.RemoteConfig()

			// Pull from remote before processing (if remote is configured)
			if localStore.IsRemoteEnabled() {
				if pullErr := localStore.Pull(ctx); pullErr != nil {
					return fmt.Errorf("pull from remote: %w", pullErr)
				}
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
							if commitErr := commitAndPush(ctx, crawler, localStore, remoteConfig, "periodic sync"); commitErr != nil {
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
				if commitErr := commitAndPush(ctx, crawler, localStore, remoteConfig, "sync complete"); commitErr != nil {
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
			storePath := resolveStorePath(cmd)
			remoteConfig := store.LoadRemoteConfigFromEnv()

			localStore, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
			if err != nil {
				return fmt.Errorf("create store: %w", err)
			}

			// Create crawler (no client needed for list)
			crawler := sync.NewCrawler(nil, localStore, sync.WithCrawlerLogger(slog.Default()))

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
			storePath := resolveStorePath(cmd)
			remoteConfig := store.LoadRemoteConfigFromEnv()

			localStore, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
			if err != nil {
				return fmt.Errorf("create store: %w", err)
			}

			// Create crawler (no client needed for status)
			crawler := sync.NewCrawler(nil, localStore, sync.WithCrawlerLogger(slog.Default()))

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
			storePath := resolveStorePath(cmd)
			remoteConfig := store.LoadRemoteConfigFromEnv()

			st, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
			if err != nil {
				return fmt.Errorf("create store: %w", err)
			}

			crawler := sync.NewCrawler(nil, st, sync.WithCrawlerLogger(slog.Default()))
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
			storePath := resolveStorePath(cmd)
			remoteConfig := store.LoadRemoteConfigFromEnv()

			localStore, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
			if err != nil {
				return fmt.Errorf("create store: %w", err)
			}

			// Create crawler (no client needed for cleanup)
			crawler := sync.NewCrawler(nil, localStore, sync.WithCrawlerLogger(slog.Default()))

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
				if err := commitAndPush(ctx, crawler, localStore, remoteConfig, "cleanup orphaned pages"); err != nil {
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
			storePath := resolveStorePath(cmd)
			remoteConfig := store.LoadRemoteConfigFromEnv()

			localStore, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
			if err != nil {
				return fmt.Errorf("create store: %w", err)
			}

			// Create queue manager
			queueMgr := queue.NewManager(localStore, slog.Default())

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
				crawler := sync.NewCrawler(client, localStore, sync.WithCrawlerLogger(slog.Default()))

				// Reconcile root.md at startup
				if reconcileErr := crawler.ReconcileRootMd(ctx); reconcileErr != nil {
					return fmt.Errorf("reconcile root.md: %w", reconcileErr)
				}

				opts := []webhook.SyncWorkerOption{}
				if cfg.SyncDelay > 0 {
					opts = append(opts, webhook.WithSyncDelay(cfg.SyncDelay))
				}

				syncWorker = webhook.NewSyncWorker(crawler, localStore, remoteConfig, slog.Default(), opts...)
				slog.Info("auto-sync enabled", "sync_delay", cfg.SyncDelay)
			} else if cfg.AutoSync {
				slog.Warn("auto-sync disabled: NOTION_TOKEN not configured")
			}

			// Create and start server
			server := webhook.NewServer(cfg, queueMgr, localStore, slog.Default(), syncWorker, remoteConfig)

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

// setupClientAndStore creates the Notion client and store from command flags.
func setupClientAndStore(cmd *cli.Command) (*notion.Client, store.Store, error) {
	token := cmd.String("token")
	if token == "" {
		token = os.Getenv("NOTION_TOKEN")
	}
	if token == "" {
		return nil, nil, apperrors.ErrNotionTokenRequired
	}

	storePath := resolveStorePath(cmd)

	// Load remote config from environment
	remoteConfig := store.LoadRemoteConfigFromEnv()

	client := notion.NewClient(token)
	st, err := store.NewLocalStore(storePath, store.WithRemoteConfig(remoteConfig))
	if err != nil {
		return nil, nil, fmt.Errorf("create store: %w", err)
	}

	return client, st, nil
}

// printPageFlat prints a page in flat list format.
//
//nolint:forbidigo // CLI user output function
func printPageFlat(page *sync.PageInfo) {
	timeSince := formatTimeSince(page.LastSynced)
	orphanedMark := ""
	if page.IsOrphaned {
		orphanedMark = " (ORPHANED - parent deleted)"
	}

	fmt.Printf("  %s - \"%s\" (last synced: %s)%s\n",
		page.Path,
		page.Title,
		timeSince,
		orphanedMark)
}

// printPageTree prints a page in tree format.
//
//nolint:forbidigo // CLI user output function
func printPageTree(page *sync.PageInfo, prefix string, isLast bool) {
	// Determine the tree characters
	var branch, nextPrefix string
	if isLast {
		branch = "└── "
		nextPrefix = prefix + "    "
	} else {
		branch = "├── "
		nextPrefix = prefix + "│   "
	}

	timeSince := formatTimeSince(page.LastSynced)
	orphanedMark := ""
	if page.IsOrphaned {
		orphanedMark = " (ORPHANED)"
	}

	filename := page.Path
	if idx := strings.LastIndex(page.Path, "/"); idx != -1 {
		filename = page.Path[idx+1:]
	}

	fmt.Printf("%s%s - \"%s\" (last synced: %s)%s\n",
		prefix+branch,
		filename,
		page.Title,
		timeSince,
		orphanedMark)

	// Print children
	for i, child := range page.Children {
		printPageTree(child, nextPrefix, i == len(page.Children)-1)
	}
}

// displayFolderStatus displays status for a specific folder.
//
//nolint:forbidigo // CLI user output function
func displayFolderStatus(folder string, status *sync.StatusInfo) {
	fmt.Printf("Notion Sync Status - %s folder\n\n", folder)

	folderStatus, exists := status.Folders[folder]
	if !exists {
		fmt.Printf("Folder '%s' not found\n", folder)
		return
	}

	fmt.Printf("Pages: %d (%d root pages)\n", folderStatus.PageCount, folderStatus.RootPages)

	if folderStatus.LastSynced != nil {
		fmt.Printf("Last sync: %s\n", formatTimeSince(*folderStatus.LastSynced))
	} else {
		fmt.Println("Last sync: never")
	}

	// Queue info for this folder
	queuedInit := 0
	queuedUpdate := 0
	for _, q := range status.QueueEntries {
		if q.Type == "init" {
			queuedInit += q.PageCount
		} else {
			queuedUpdate += q.PageCount
		}
	}

	totalQueued := queuedInit + queuedUpdate
	if totalQueued > 0 {
		fmt.Printf("Queue: %d pages pending (%d init, %d update)\n", totalQueued, queuedInit, queuedUpdate)
		fmt.Println("\nQueue files:")
		for _, q := range status.QueueEntries {
			fmt.Printf("  - %s: %d pages (%s)\n", q.QueueFile, q.PageCount, q.Type)
		}
	} else {
		fmt.Println("Queue: empty")
	}
}

// displayOverallStatus displays overall status across all folders.
//
//nolint:forbidigo // CLI user output function
func displayOverallStatus(status *sync.StatusInfo) {
	fmt.Println("Notion Sync Status")
	fmt.Println()

	if status.FolderCount == 0 {
		fmt.Println("No folders found. Add entries to root.md to configure root pages.")
		return
	}

	// Get folder names
	var folderNames []string
	for name := range status.Folders {
		folderNames = append(folderNames, name)
	}

	fmt.Printf("Folders: %d (%s)\n", status.FolderCount, strings.Join(folderNames, ", "))
	fmt.Printf("Total pages: %d\n", status.TotalPages)
	fmt.Printf("Root pages: %d\n\n", status.TotalRootPages)

	// Queue summary
	if len(status.QueueEntries) > 0 {
		displayQueueSummary(status)
	} else {
		fmt.Println("Queue: empty")
	}

	fmt.Println("\nLast sync:")
	for _, folderStatus := range status.Folders {
		if folderStatus.LastSynced != nil {
			fmt.Printf("  %s: %s\n", folderStatus.Name, formatTimeSince(*folderStatus.LastSynced))
		} else {
			fmt.Printf("  %s: never\n", folderStatus.Name)
		}
	}
}

// displayQueueSummary displays the queue summary for overall status.
//
//nolint:forbidigo // CLI user output function
func displayQueueSummary(status *sync.StatusInfo) {
	totalQueued := 0
	queueByFolder := make(map[string]struct{ init, update int })

	for _, queueEntry := range status.QueueEntries {
		totalQueued += queueEntry.PageCount
		stats := queueByFolder[queueEntry.Folder]
		if queueEntry.Type == "init" {
			stats.init += queueEntry.PageCount
		} else {
			stats.update += queueEntry.PageCount
		}
		queueByFolder[queueEntry.Folder] = stats
	}

	fmt.Printf("Queue:\n")
	fmt.Printf("  Pending: %d pages across %d queue files\n", totalQueued, len(status.QueueEntries))

	for folderName, stats := range queueByFolder {
		fmt.Printf("    - %s: %d pages (%d init, %d update)\n",
			folderName, stats.init+stats.update, stats.init, stats.update)
	}

	fmt.Println("\nNext sync will process:")
	for _, queueEntry := range status.QueueEntries {
		fmt.Printf("  - %s: %d pages (%s, %s)\n",
			queueEntry.QueueFile, queueEntry.PageCount, queueEntry.Folder, queueEntry.Type)
	}
}

// displayCleanupResults displays the results of a cleanup operation.
//
//nolint:forbidigo // CLI user output function
func displayCleanupResults(result *sync.CleanupResult, dryRun bool) {
	fmt.Printf("\nCleanup Results:\n")
	fmt.Printf("  Orphaned pages found: %d\n", result.OrphanedPages)

	if dryRun {
		fmt.Printf("\nDry run - no changes were made\n")
	} else {
		fmt.Printf("  Registries deleted: %d\n", result.DeletedRegistries)
		fmt.Printf("  Files deleted: %d\n", result.DeletedFiles)
	}
}

// displayPullResults displays the results of a pull operation.
//
//nolint:forbidigo // CLI user output function
func displayPullResults(result *sync.PullResult, showAll, dryRun bool) {
	fmt.Printf("\nPull Results:\n")
	fmt.Printf("  Cutoff time: %s\n", result.CutoffTime.Format(time.RFC3339))
	fmt.Printf("  Pages found: %d\n", result.PagesFound)
	fmt.Printf("  Pages queued: %d\n", result.PagesQueued)
	if showAll {
		fmt.Printf("    - New pages: %d\n", result.NewPages)
		fmt.Printf("    - Updated pages: %d\n", result.UpdatedPages)
	}
	fmt.Printf("  Pages skipped: %d\n", result.PagesSkipped)

	if dryRun {
		fmt.Printf("\nDry run - no changes were made\n")
	} else {
		fmt.Printf("\nPages have been queued. Run 'sync' to download them.\n")
	}
}

// displayRemoteConfig displays the remote git configuration.
//
//nolint:forbidigo // CLI user output function
func displayRemoteConfig(cfg *store.RemoteConfig) {
	fmt.Println("Remote Git Configuration")
	fmt.Println()

	// Show storage mode
	effectiveMode := cfg.EffectiveStorageMode()
	if cfg.Storage == "" {
		fmt.Printf("Storage:  %s (auto-detected)\n", effectiveMode)
	} else {
		fmt.Printf("Storage:  %s\n", effectiveMode)
	}

	if effectiveMode == store.StorageModeLocal {
		fmt.Println("\nRemote operations disabled (local-only mode)")
		if cfg.URL != "" {
			fmt.Printf("URL:      %s (ignored due to NTN_STORAGE=local)\n", cfg.URL)
		}
		return
	}

	if cfg.URL == "" {
		fmt.Println("\nRemote: not configured (set NTN_GIT_URL to enable)")
		return
	}

	fmt.Printf("URL:      %s\n", cfg.URL)
	if cfg.IsSSH() {
		fmt.Println("Auth:     SSH (using ssh-agent)")
	} else {
		if cfg.Password != "" {
			fmt.Println("Auth:     HTTPS (token configured)")
		} else {
			fmt.Println("Auth:     HTTPS (WARNING: NTN_GIT_PASS not set)")
		}
	}
	fmt.Printf("Branch:   %s\n", cfg.Branch)
	fmt.Printf("User:     %s\n", cfg.User)
	fmt.Printf("Email:    %s\n", cfg.Email)

	// Show NTN_DIR if set
	ntnDir := os.Getenv("NTN_DIR")
	if ntnDir != "" {
		fmt.Printf("Dir:      %s (from NTN_DIR)\n", ntnDir)
	}
}

// displayConnectionTest tests the connection and displays the result.
//
//nolint:forbidigo // CLI user output function
func displayConnectionTest(ctx context.Context, cfg *store.RemoteConfig) error {
	fmt.Printf("Testing connection to %s...\n", cfg.URL)

	if testErr := cfg.TestConnection(ctx); testErr != nil {
		return fmt.Errorf("connection test failed: %w", testErr)
	}

	fmt.Println("Connection successful!")
	return nil
}

// displayScanComplete displays the scan complete message.
//
//nolint:forbidigo // CLI user output function
func displayScanComplete() {
	fmt.Println("\nScan complete. Run 'sync' to download the queued child pages.")
}

// displayNoFoldersMessage displays the no folders found message.
//
//nolint:forbidigo // CLI user output function
func displayNoFoldersMessage() {
	fmt.Println("No folders found. Add entries to root.md to configure root pages.")
}

// displayPageList displays the list of pages in folders.
//
//nolint:forbidigo // CLI user output function
func displayPageList(folders []*sync.FolderInfo, tree bool) {
	for _, folderInfo := range folders {
		orphanedNote := ""
		if folderInfo.OrphanedPages > 0 {
			orphanedNote = fmt.Sprintf(", %d orphaned", folderInfo.OrphanedPages)
		}
		fmt.Printf("%s (%d root pages, %d total pages%s)\n",
			folderInfo.Name,
			folderInfo.RootPages,
			folderInfo.TotalPages,
			orphanedNote)

		if tree {
			for _, page := range folderInfo.Pages {
				printPageTree(page, "", true)
			}
		} else {
			for _, page := range folderInfo.Pages {
				printPageFlat(page)
			}
		}
		fmt.Println()
	}
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

// commitAndPush commits changes and optionally pushes to remote.
func commitAndPush(
	ctx context.Context, crawler *sync.Crawler, localStore *store.LocalStore, cfg *store.RemoteConfig, reason string,
) error {
	message := fmt.Sprintf("[ntnsync] %s at %s", reason, time.Now().Format(time.RFC3339))
	if err := crawler.CommitChanges(ctx, message); err != nil {
		slog.WarnContext(ctx, "failed to commit changes", "error", err, "reason", reason)
		return nil // Don't fail the sync for commit errors
	}

	// Push if enabled
	if cfg.IsPushEnabled() {
		if err := localStore.Push(ctx); err != nil {
			return fmt.Errorf("push to remote: %w", err)
		}
	}

	return nil
}

// formatTimeSince formats a time duration in a human-readable way.
func formatTimeSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	duration := time.Since(t)

	switch {
	case duration < time.Minute:
		return "just now"
	case duration < time.Hour:
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	case duration < hoursPerDay*time.Hour:
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case duration < daysPerWeek*hoursPerDay*time.Hour:
		days := int(duration.Hours() / hoursPerDay)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case duration < daysPerMonth*hoursPerDay*time.Hour:
		weeks := int(duration.Hours() / hoursPerDay / daysPerWeek)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		months := int(duration.Hours() / hoursPerDay / daysPerMonth)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}
