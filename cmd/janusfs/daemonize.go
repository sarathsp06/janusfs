package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/control"
)

// This file owns running the daemon as a background service: detaching from the
// terminal, redirecting output to a log file, and tailing that log. It is split
// out of daemon.go so the daemonize/logs plumbing is separate from the running
// daemon's request loop.

// daemonLogPath is ~/.janusfs/logs/daemon.log, where a --background daemon
// sends its stdout/stderr. Deliberately not under ~/.janusfs/run/, which
// internal/health scans treating every file there as a mount.
func daemonLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("daemon: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "logs", "daemon.log"), nil
}

// startDaemonBackground re-execs `janusfs daemon` as a detached (setsid)
// process with stdout/stderr redirected to daemonLogPath, then waits for its
// control socket to answer before returning. This is the Go-idiomatic
// stand-in for the classic double-fork daemonize: the child is a fresh
// process (same setsid pattern as spawnWatchdog), so there are no inherited
// runtime threads to reason about. The socket, not a pidfile, is the single
// source of truth for "is a daemon running" — runDaemon already guards on it.
func startDaemonBackground(debug bool, indexPort int) error {
	sock, err := control.SocketPath()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if conn, derr := net.Dial("unix", sock); derr == nil {
		_ = conn.Close()
		fmt.Printf("JanusFS daemon is already running.\n  Dashboard: http://127.0.0.1:%d/\n", indexPort)
		return nil
	}

	logPath, err := daemonLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("daemon: creating log dir: %w", err)
	}
	// ponytail: single append-only log file, no rotation. Add lumberjack (or a
	// size check on open) if it grows unbounded in practice.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("daemon: opening %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: resolving own executable: %w", err)
	}
	// A detached daemon has no interactive session to open a browser into, so
	// the background child always runs --no-open regardless of the parent flag.
	args := []string{"daemon", "--ui-port", strconv.Itoa(indexPort), "--no-open"}
	if debug {
		args = append(args, "--debug")
	}
	child := exec.Command(exe, args...)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout = logFile
	child.Stderr = logFile
	if err := child.Start(); err != nil {
		return fmt.Errorf("daemon: starting background process: %w", err)
	}
	// Release, don't Wait: the child must outlive this launcher.
	_ = child.Process.Release()

	// Startup grace: poll the socket until the child binds it or we time out.
	for range 50 {
		if conn, derr := net.Dial("unix", sock); derr == nil {
			_ = conn.Close()
			fmt.Printf("JanusFS daemon started (pid %d).\n  Logs:      %s\n  Dashboard: http://127.0.0.1:%d/\n", child.Process.Pid, logPath, indexPort)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon: background process started (pid %d) but its control socket did not come up within 5s; check %s", child.Process.Pid, logPath)
}

func newLogsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show the background daemon's log (~/.janusfs/logs/daemon.log)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := daemonLogPath()
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("logs: no daemon log at %s (start one with `janusfs daemon --background`)", path)
			}
			tailArgs := []string{"-n", "200"}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			c := exec.Command("tail", append(tailArgs, path)...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the log as it grows (like tail -f)")
	return cmd
}
