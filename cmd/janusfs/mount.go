package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/logging"
	"github.com/sarathsp06/janusfs/internal/mount"
	"github.com/spf13/cobra"
)

// shutdownGrace is the FR-2 "≤ 5 s, then force" drain window for a clean
// unmount on SIGINT/SIGTERM.
const shutdownGrace = 5 * time.Second

func newMountCmd() *cobra.Command {
	cfg := config.Default()
	if err := config.ApplyEnv(&cfg); err != nil {
		// Env parsing failure is a startup-time configuration error, not a
		// runtime one; fail closed by aborting before any flag is even
		// registered rather than silently falling back to built-in
		// defaults (SPEC §20.2).
		fmt.Fprintln(os.Stderr, "janusfs: "+err.Error())
		os.Exit(1)
	}

	var debug bool

	cmd := &cobra.Command{
		Use:   "mount <src> <mountpoint>",
		Short: "Mount a sanitized view of <src> at <mountpoint>",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.Src, cfg.Mountpoint = args[0], args[1]
			return runMount(cmd, cfg, debug)
		},
	}

	// Flag defaults are the post-ApplyEnv values, so an explicit flag wins
	// over an env var which wins over the built-in default (SPEC §15),
	// matching docs/SPEC_AMENDMENTS.md (2026-07-17)'s cobra/pflag design.
	cmd.Flags().IntVar(&cfg.UIPort, "ui-port", cfg.UIPort, "localhost port for the dashboard/API")
	cmd.Flags().Int64Var(&cfg.CacheMaxBytes, "cache-max-bytes", cfg.CacheMaxBytes, "RAM-cache budget in bytes")
	cmd.Flags().Int64Var(&cfg.CacheMaxFile, "cache-max-file", cfg.CacheMaxFile, "per-entry cache size cap in bytes")
	cmd.Flags().IntVar(&cfg.HistoryRetentionDays, "history-retention", cfg.HistoryRetentionDays, "days of history rollups to keep")
	cmd.Flags().BoolVar(&cfg.NoHistory, "no-history", cfg.NoHistory, "disable history persistence entirely")
	cmd.Flags().Int64Var(&cfg.RedactBufferMax, "redact-buffer-max", cfg.RedactBufferMax, "whole-file buffering cap in bytes for unbounded custom regexes")
	cmd.Flags().BoolVar(&debug, "debug", false, "verbose debug logging")

	return cmd
}

func runMount(cmd *cobra.Command, cfg config.Config, debug bool) error {
	if err := cfg.Validate(); err != nil {
		// FR-30: one-line cause, no raw stack traces. cfg.Validate's errors
		// are already single-line and user-facing (SPEC §15/FR-1).
		return fmt.Errorf("mount: %w", err)
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logging.SetOutput(os.Stderr, level)
	logger := logging.New("mount")

	// jacobsa/fuse's MountConfig wants *log.Logger, not *slog.Logger; bridge
	// the two so FUSE wire/debug/error logs still flow through the single
	// configured destination (SPEC §21: no bare log destinations).
	errLog := log.New(logWriter{logger, slog.LevelError}, "", 0)
	var dbgLog *log.Logger
	if debug {
		dbgLog = log.New(logWriter{logger, slog.LevelDebug}, "", 0)
	}

	if err := writePidfile(cfg.Mountpoint); err != nil {
		// Non-fatal per FR-3's spirit (the pidfile is a discoverability aid
		// for umount, not a correctness requirement for the mount itself),
		// but the user should know umount may not find this process later.
		logger.Warn("failed to write pidfile", "error", err)
	}
	defer func() {
		if err := removePidfile(cfg.Mountpoint); err != nil {
			logger.Warn("failed to remove pidfile", "error", err)
		}
	}()

	adapter := &mount.Adapter{
		ErrorLogger: errLog,
		DebugLogger: dbgLog,
		OnMounted: func() {
			printStatusBlock(cfg)
		},
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received, unmounting")
		cancel()
		// FR-2: force after the grace window if the clean unmount hasn't
		// completed (e.g. a wedged FUSE op). Mount's own ctx.Done() path
		// already issues fuse.Unmount; this is the backstop.
		go func() {
			time.Sleep(shutdownGrace)
			logger.Warn("unmount did not complete within grace period, forcing")
			_ = adapter.Unmount(cfg.Mountpoint)
		}()
	}()

	if err := adapter.Mount(ctx, cfg.Src, cfg.Mountpoint); err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	return nil
}

// printStatusBlock implements FR-31: a concise post-mount status block
// naming the source, mountpoint, and (once Phase 4 lands) the dashboard
// URL — printed the moment the mount is actually ready, via
// mount.Adapter.OnMounted, not merely attempted.
func printStatusBlock(cfg config.Config) {
	fmt.Printf("Mounted %s -> %s\n", cfg.Src, cfg.Mountpoint)
	fmt.Printf("  ui-port: %d (dashboard not yet implemented — Phase 4)\n", cfg.UIPort)
}

// logWriter adapts an *slog.Logger to io.Writer so stdlib *log.Logger
// consumers (like jacobsa/fuse's MountConfig) can be routed through the
// single configured slog destination (SPEC §15/§21) instead of writing
// directly to a file descriptor.
type logWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logger.Log(context.Background(), w.level, string(p))
	return len(p), nil
}
