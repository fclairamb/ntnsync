// Package cmd provides the CLI commands for notion-sync.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/store"
	"github.com/fclairamb/ntnsync/internal/sync"
)

const (
	// Time duration constants for relative time formatting.
	hoursPerDay  = 24
	daysPerWeek  = 7
	daysPerMonth = 30
)

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
