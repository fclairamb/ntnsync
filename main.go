// Package main is the entry point for notion-git-sync.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fclairamb/ntnsync/internal/cmd"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("received shutdown signal")
		cancel()
	}()

	// Run the CLI
	app := cmd.NewApp()
	if err := app.Run(ctx, os.Args); err != nil {
		slog.Error("error", "error", err)
		return 1
	}

	return 0
}
