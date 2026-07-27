package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/logging"
)

// watchdogPidAlive is pidAlive behind a package-level indirection, matching
// the existing unmountCommand/runtimeGOOS/mountpointMounted pattern in
// umount.go, so tests can simulate the supervised daemon dying without
// needing a real process.
var watchdogPidAlive = pidAlive

func newWatchdogCmd() *cobra.Command {
	var daemonPid int
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "watchdog",
		Short: "Supervise the daemon and force-unmount its mounts if it dies",
		Long: "Polls the given daemon PID for liveness. If the daemon disappears without\n" +
			"running its own shutdown path (SIGKILL, an OOM kill, a panic that escapes),\n" +
			"a FUSE mount is left attached with no server behind it and the kernel does not\n" +
			"fall back to the real filesystem — every process touching that path hangs or\n" +
			"gets EIO. This command detects that and force-unmounts every recorded mount\n" +
			"that is still mounted, restoring native access with no user action needed.\n\n" +
			"Spawned automatically, detached, by `janusfs daemon` at startup. Not intended\n" +
			"to be run by hand.",
		Hidden: true, // spawned by the daemon; a human invocation can't supply a meaningful --pid
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatchdog(cmd.Context(), daemonPid, interval)
		},
	}
	cmd.Flags().IntVar(&daemonPid, "pid", 0, "PID of the daemon to supervise (required)")
	cmd.Flags().DurationVar(&interval, "interval", 500*time.Millisecond, "liveness poll interval")
	return cmd
}

// runWatchdog polls daemonPid for liveness every interval until either the
// context is cancelled (a clean shutdown signalled this watchdog directly) or
// the daemon is found dead, at which point it sweeps stale mounts once and
// returns.
func runWatchdog(ctx context.Context, daemonPid int, interval time.Duration) error {
	if daemonPid <= 0 {
		return fmt.Errorf("watchdog: --pid is required and must be a positive process ID")
	}

	logger := logging.New("watchdog")

	if !watchdogPidAlive(daemonPid) {
		return fmt.Errorf("watchdog: daemon pid %d is not alive at startup; nothing to supervise", daemonPid)
	}
	logger.Info("watchdog started", "daemon_pid", daemonPid, "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("watchdog stopping (signalled directly, daemon shut down cleanly)")
			return nil
		case <-ticker.C:
			if watchdogPidAlive(daemonPid) {
				continue
			}
			logger.Info("daemon no longer alive; sweeping recorded mounts", "daemon_pid", daemonPid)
			sweepStaleMounts(logger)
			return nil
		}
	}
}

// sweepStaleMounts force-unmounts every mount recorded in the mounts registry
// that is STILL mounted. A clean daemon shutdown has already unmounted
// everything before exiting, so in that case this sweep finds nothing mounted
// and does nothing — the mountpointMounted guard is the entire coordination
// protocol between a graceful shutdown and this watchdog. No lease file,
// handshake, or shutdown notification is needed or should be added: the
// guard is what makes the ungraceful-death and clean-shutdown paths share
// this exact same code safely.
func sweepStaleMounts(logger *slog.Logger) {
	records, err := config.LoadMounts()
	if err != nil {
		if logger != nil {
			logger.Warn("watchdog: failed to read mounts registry", "error", err)
		}
		return
	}
	for _, rec := range records {
		if !mountpointMounted(rec.Mountpoint) {
			continue // clean shutdown already unmounted this one
		}
		if err := unmountKernel(rec.Mountpoint, true); err != nil {
			if logger != nil {
				logger.Warn("watchdog: force unmount failed", "mountpoint", rec.Mountpoint, "error", err)
			}
			continue
		}
		if logger != nil {
			logger.Info("watchdog: force-unmounted a mount left behind by an ungraceful daemon death", "mountpoint", rec.Mountpoint)
		}
	}
}

// watchdogPidfilePath is ~/.janusfs/watchdog.pid — deliberately NOT inside
// ~/.janusfs/run/, which internal/health.Run scans treating every *.pid file
// as one mount; colocating the watchdog's own pidfile there would make it show
// up as a phantom mount in `janusfs doctor`.
func watchdogPidfilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("watchdog: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "watchdog.pid"), nil
}

// spawnWatchdog is called once by runDaemon after the control socket is bound
// and before the accept loop starts. It starts a detached
// `janusfs watchdog --pid <this-daemon's-pid>` process to supervise this
// daemon, and records its PID both on d (for the shutdown courtesy signal in
// stopWatchdog) and in watchdogPidfilePath (for `janusfs doctor` to report).
//
// A spawn failure only logs a warning and leaves d.watchdogPID at its zero
// value: a missing watchdog degrades crash recovery to manual, and must never
// prevent the daemon itself from starting.
func spawnWatchdog(d *daemon, logger *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		logger.Warn("watchdog: could not resolve own executable path; crash recovery will be manual", "error", err)
		return
	}

	cmd := exec.Command(exe, "watchdog", "--pid", strconv.Itoa(os.Getpid()))
	// Setsid: a terminal SIGINT reaching this daemon's process group would
	// otherwise reach the watchdog at the same instant, and the watchdog would
	// die before it could observe the death it exists to observe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Warn("watchdog: failed to start; crash recovery will be manual", "error", err)
		return
	}

	d.watchdogPID = cmd.Process.Pid
	logger.Info("watchdog spawned", "watchdog_pid", d.watchdogPID)

	if path, err := watchdogPidfilePath(); err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr == nil {
			_ = os.WriteFile(path, []byte(strconv.Itoa(d.watchdogPID)), 0o600)
		}
	}

	// A started *exec.Cmd's Wait must eventually be called or the child leaks
	// as a zombie once it exits (on its own sweep-and-exit, or via
	// stopWatchdog's SIGTERM below); nothing here needs the result.
	go func() { _ = cmd.Wait() }()
}

// stopWatchdog signals a courtesy SIGTERM to the spawned watchdog (if any) and
// removes its pidfile. Called from daemon.shutdown() after every mount is
// already unmounted, so this is an optimization — a surviving watchdog's own
// liveness sweep would find nothing mounted regardless — not a correctness
// requirement.
func stopWatchdog(d *daemon) {
	if d.watchdogPID > 0 {
		_ = syscall.Kill(d.watchdogPID, syscall.SIGTERM)
	}
	if path, err := watchdogPidfilePath(); err == nil {
		_ = os.Remove(path)
	}
}
