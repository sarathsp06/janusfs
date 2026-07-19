package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sarathsp06/janusfs/internal/api"
	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/history"
	"github.com/sarathsp06/janusfs/internal/logging"
	"github.com/sarathsp06/janusfs/internal/mount"
	"github.com/sarathsp06/janusfs/internal/obs"
	"github.com/sarathsp06/janusfs/internal/provider"
	"github.com/sarathsp06/janusfs/internal/ui"
	"github.com/sarathsp06/janusfs/internal/watch"
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
	var noOpen bool

	cmd := &cobra.Command{
		Use:   "mount <src> [mountpoint]",
		Short: "Mount a sanitized view of <src> at <mountpoint> (or a derived path under --mount-root)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.Src = args[0]
			if len(args) == 2 {
				cfg.Mountpoint = args[1]
			}
			return runMount(cmd, cfg, debug, noOpen)
		},
	}

	// Flag defaults are the post-ApplyEnv values, so an explicit flag wins
	// over an env var which wins over the built-in default (SPEC §15),
	// matching docs/SPEC_AMENDMENTS.md (2026-07-17)'s cobra/pflag design.
	cmd.Flags().IntVar(&cfg.UIPort, "ui-port", cfg.UIPort, "port for the dashboard/API (binds 0.0.0.0)")
	cmd.Flags().Int64Var(&cfg.CacheMaxBytes, "cache-max-bytes", cfg.CacheMaxBytes, "RAM-cache budget in bytes")
	cmd.Flags().Int64Var(&cfg.CacheMaxFile, "cache-max-file", cfg.CacheMaxFile, "per-entry cache size cap in bytes")
	cmd.Flags().IntVar(&cfg.HistoryRetentionDays, "history-retention", cfg.HistoryRetentionDays, "days of history rollups to keep")
	cmd.Flags().BoolVar(&cfg.NoHistory, "no-history", cfg.NoHistory, "disable history persistence entirely")
	cmd.Flags().Int64Var(&cfg.RedactBufferMax, "redact-buffer-max", cfg.RedactBufferMax, "whole-file buffering cap in bytes for unbounded custom regexes")
	cmd.Flags().StringVar(&cfg.MountRoot, "mount-root", cfg.MountRoot, "directory under which a mountpoint is derived when [mountpoint] is omitted")
	cmd.Flags().StringVar(&cfg.Name, "name", "", "leaf name for the derived mountpoint (default: basename of <src>)")
	cmd.Flags().BoolVar(&debug, "debug", false, "verbose debug logging")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the dashboard in a browser after mounting")

	return cmd
}

func runMount(cmd *cobra.Command, cfg config.Config, debug, noOpen bool) error {
	if err := cfg.ResolveMountpoint(); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	// A mountpoint whose source directory is itself empty stays reported as
	// "empty" by cfg.Validate() even while it is already live-mounted (the
	// passthrough view of an empty source is, correctly, empty) — Validate's
	// non-empty check alone cannot catch that case, and mounting on top of
	// an already-mounted directory hangs rather than failing. Check the
	// pidfile registry (FR-3) for a still-running owner first, for any
	// mountpoint (explicit or derived): this is a general double-mount
	// guard, not specific to --mount-root.
	if pid, err := readPidfile(cfg.Mountpoint); err == nil && pidAlive(pid) {
		return fmt.Errorf("mount: %q is already mounted by a running janusfs process (pid %d); unmount it first (janusfs umount %s) or choose a different mountpoint", cfg.Mountpoint, pid, cfg.Mountpoint)
	}

	if err := cfg.Validate(); err != nil {
		// FR-30: one-line cause, no raw stack traces. cfg.Validate's errors
		// are already single-line and user-facing (SPEC §15/FR-1). When the
		// mountpoint was derived (--mount-root), a non-empty-directory error
		// means a leaf collision with a live mount (SPEC_AMENDMENTS.md
		// 2026-07-18) — add the fail-closed remedy rather than leaving the
		// user to guess why an empty dir "isn't empty".
		if cfg.MountRoot != "" && errors.Is(err, config.ErrMountpointNotEmpty) {
			return fmt.Errorf("mount: derived mountpoint %q is in use by another mount; pass an explicit [mountpoint] or --name to pick a different one: %w", cfg.Mountpoint, err)
		}
		return fmt.Errorf("mount: %w", err)
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logging.SetOutput(os.Stderr, level)
	logger := logging.New("mount")

	// hanwen/go-fuse's fs.Options/MountOptions want *log.Logger, not
	// *slog.Logger; bridge the two so FUSE wire/debug/error logs still flow
	// through the single configured destination (SPEC §21: no bare log
	// destinations).
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
	} else {
		v, _ := pidfilePath(cfg.Mountpoint)
		logger.Info("wrote pid file", "path", v)
	}

	defer func() {
		if err := removePidfile(cfg.Mountpoint); err != nil {
			logger.Warn("failed to remove pidfile", "error", err)
		}
	}()

	// SPEC §15 step 5/6: construct internal/rules (initial compile) ->
	// internal/engine, then internal/provider wired to the same
	// cache-sizing tunables cfg already carries.
	eng, err := engine.New(cfg.Src)
	if err != nil {
		return fmt.Errorf("mount: compiling rule set: %w", err)
	}
	prov := provider.NewRamCache(cfg.CacheMaxBytes, cfg.CacheMaxFile, cfg.RedactBufferMax)

	// SPEC Phase 4: observability components (FR-22/FR-23/FR-31).
	eventBus := obs.NewEventBus(4096)
	metrics := &obs.JanusMetrics{}
	ringBuf := obs.NewRingBuffer(8192)
	topN := obs.NewTopN(1000)

	// Per-mount random bearer token for dashboard/API auth (SPEC §11).
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("mount: generating bearer token: %w", err)
	}
	bearerToken := hex.EncodeToString(tokenBytes)

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// SPEC §15 step 7: construct internal/watch, wired to trigger rule
	// recompilation (FR-18) and provider invalidation (FR-19/FR-20).
	var wtr *watch.Watcher
	wtr, wErr := watch.New()
	if wErr != nil {
		logger.Warn("failed to create file watcher, continuing without hot-reload", "error", wErr)
		// Non-fatal: the watcher is advisory-only (FR-21). Without it,
		// the mount still works — the backstop (mtime/size/inode check on
		// every masked read) is the authoritative change detector, and
		// config changes simply aren't picked up until remount.
	} else {
		wl := logging.New("watch")

		// reloadCh is a coalescing channel (capacity 1) for config-file
		// change events. The debounce goroutine below reads from it and
		// schedules an engine.Reload + provider.InvalidateAll after 200ms
		// of inactivity (SPEC §9: "Config events → debounce 200 ms").
		reloadCh := make(chan struct{}, 1)

		wtr.Start(cfg.Src,
			// onConfig: config-file change → signal recompile.
			func(path string) {
				select {
				case reloadCh <- struct{}{}:
				default:
				}
			},
			// onData: data-file change → invalidate that path's cache.
			// An empty path signals watcher overflow/error, which should
			// trigger a full cache invalidate (SPEC §9).
			func(path string, op watch.Op) {
				if path == "" {
					prov.InvalidateAll()
					logger.Warn("watcher overflow, invalidating all cached content")
					return
				}
				prov.Invalidate(path)
			},
		)

		// Debounce goroutine for config-file changes (FR-18: 200ms debounce).
		var debounceMu sync.Mutex
		var debounceTimer *time.Timer

		go func() {
			for {
				select {
				case <-reloadCh:
					debounceMu.Lock()
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.AfterFunc(200*time.Millisecond, func() {
						debounceMu.Lock()
						debounceTimer = nil
						debounceMu.Unlock()

						wl.Info("config file changed, recompiling rule set")
						if err := eng.Reload(cfg.Src); err != nil {
							wl.Error("recompile failed after config change", "error", err)
							// Fail-closed: still invalidate cache so
							// stale masks aren't served (FR-20).
						}
						prov.InvalidateAll()
						wl.Info("rule set recompiled", "generation", eng.Generation())
					})
					debounceMu.Unlock()

				case <-ctx.Done():
					debounceMu.Lock()
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceMu.Unlock()
					return
				}
			}
		}()
	}

	metrics.Generation.Store(eng.Generation())

	// SPEC Phase 4: internal/history (SQLite rollup store, FR-41…FR-46).
	var hist *history.Store
	if !cfg.NoHistory {
		hd := historyDBPath(cfg.Src)
		var hErr error
		hist, hErr = history.Open(cfg.Src, hd, cfg.HistoryRetentionDays)
		if hErr != nil {
			logger.Warn("failed to open history DB, continuing without persistence", "error", hErr)
		} else if hist != nil {
			logger.Info("history DB opened", "path", hd)
		}
	}

	// SPEC Phase 4: internal/api server (REST + SSE + embedded UI).
	apiSrv := api.New(ui.FS, bearerToken, metrics, ringBuf, topN, eventBus, hist)
	apiSrv.SetVFSMeta(cfg.Src, func() (int, int64, uint64, uint64, uint64) {
		ps := prov.Stats()
		return ps.Entries, ps.Bytes, ps.Hits, ps.Misses, ps.Rebuilds
	}, func(relPath string, isDir bool) (string, []string, string) {
		res := eng.Resolve(relPath, isDir)
		return res.Decision.String(), res.PatternNames, res.RuleRef
	}, func() bool { return wtr != nil })
	// Bind synchronously before mounting: the dashboard must be live the
	// moment the status block prints its URL, and a port collision (e.g. a
	// second mount on the default --ui-port) must fail fast here rather
	// than printing a URL that points at another mount's server.
	apiAddr := fmt.Sprintf("0.0.0.0:%d", cfg.UIPort)
	apiLn, err := apiSrv.Listen(apiAddr)
	if err != nil {
		return fmt.Errorf("mount: dashboard port %d is unavailable (another janusfs mount? pass --ui-port to pick a different one): %w", cfg.UIPort, err)
	}
	go func() {
		al := logging.New("api")
		al.Info("starting API server", "addr", apiAddr)
		if err := apiSrv.Serve(apiLn); err != nil && err != http.ErrServerClosed {
			al.Error("API server error", "error", err)
		}
	}()

	// Event-bus fan-out goroutine: drains every event into each obs
	// component and the history store. FUSE handlers never block on
	// observability (FR-22).
	go func() {
		for e := range eventBus.Events() {
			metrics.RecordOp(e.Op, e.Decision)
			if e.Bytes > 0 {
				metrics.RecordBytes(e.Decision, e.Bytes)
			}
			if e.LatencyUs > 0 {
				metrics.RecordLatency(e.Op, e.LatencyUs)
			}
			switch e.Cache {
			case obs.CacheHit:
				metrics.CacheHit.Add(1)
			case obs.CacheMiss:
				metrics.CacheMiss.Add(1)
			case obs.CacheRebuild:
				metrics.CacheRebuild.Add(1)
			}
			ringBuf.Push(e.Label())
			if e.Decision == obs.Masked || e.Decision == obs.Allowed {
				topN.Record(e.Path, e.Bytes)
			}
			if hist != nil {
				hist.Record(e)
			}
		}
	}()

	adapter := &mount.Adapter{
		Engine:      eng,
		Provider:    prov,
		ErrorLogger: errLog,
		DebugLogger: dbgLog,
		OnMounted: func() {
			logger.Info("successfully mounted", "src", cfg.Src, "mountpoint", cfg.Mountpoint)
			printStatusBlock(cfg, bearerToken)
			if !noOpen {
				url := fmt.Sprintf("http://localhost:%d/?token=%s", cfg.UIPort, bearerToken)
				// Best-effort: a headless/SSH session has no browser and
				// the printed URL above is the fallback.
				if err := exec.Command("open", url).Start(); err != nil {
					logger.Warn("failed to open dashboard in browser", "error", err)
				}
			}
		},
		Observe: func(evt mount.OpEvent) {
			var d obs.Decision
			switch evt.Decision {
			case "ALLOWED":
				d = obs.Allowed
			case "MASKED":
				d = obs.Masked
			case "HIDDEN":
				d = obs.Hidden
			default:
				return
			}
			var c obs.CacheResult
			switch evt.Cache {
			case "hit":
				c = obs.CacheHit
			case "miss":
				c = obs.CacheMiss
			case "rebuild":
				c = obs.CacheRebuild
			default:
				c = obs.CacheNA
			}
			eventBus.Emit(obs.Event{
				TS:        time.Now(),
				Op:        obs.Op(evt.Op),
				Path:      evt.Path,
				Decision:  d,
				Bytes:     evt.Bytes,
				LatencyUs: evt.LatencyUs,
				Cache:     c,
			})
		},
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received, unmounting")
		cancel()
		go func() {
			time.Sleep(shutdownGrace)
			logger.Warn("unmount is taking longer than expected; if it hangs, run in another terminal: janusfs umount " + cfg.Mountpoint)
			_ = adapter.Unmount(cfg.Mountpoint)
		}()
	}()

	if err := adapter.Mount(ctx, cfg.Src, cfg.Mountpoint); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		apiSrv.Shutdown(shutdownCtx)
		cancel()
		if hist != nil {
			hist.Close()
		}
		eventBus.Close()
		return fmt.Errorf("mount: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	apiSrv.Shutdown(shutdownCtx)
	cancel()

	eventBus.Close()
	if hist != nil {
		hist.Close()
	}

	if wtr != nil {
		wtr.Stop()
	}
	return nil
}

// printStatusBlock implements FR-31: a concise post-mount status block
// naming the source, mountpoint, and (once Phase 4 lands) the dashboard
// URL — printed the moment the mount is actually ready, via
// mount.Adapter.OnMounted, not merely attempted.
// historyDBPath returns the path to the history SQLite database for src.
// It places it under ~/.janusfs/history/<basename(src)>.db.
func historyDBPath(src string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".janusfs", "history", filepath.Base(src)+".db")
}

func printStatusBlock(cfg config.Config, token string) {
	fmt.Printf("Mounted %s -> %s\n", cfg.Src, cfg.Mountpoint)
	fmt.Printf("  Dashboard: http://localhost:%d/?token=%s\n", cfg.UIPort, token)
	fmt.Printf("  API:       http://localhost:%d/api/v1/summary?token=%s\n", cfg.UIPort, token)
	fmt.Printf("  Network:   http://<this-machine-ip>:%d/?token=%s\n", cfg.UIPort, token)
}

// logWriter adapts an *slog.Logger to io.Writer so stdlib *log.Logger
// consumers (like hanwen/go-fuse's fs.Options logger) can be routed through the
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
