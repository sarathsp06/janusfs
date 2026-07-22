package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
)

func newUmountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "umount <mountpoint|src>",
		Short: "Unmount a JanusFS mount by mountpoint or source path (via the daemon, or directly if none is running)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUmount(args[0])
		},
	}
}

// runUmount asks the daemon to unmount (it owns the FUSE mount); if no daemon
// is running, or the daemon doesn't own this mountpoint, it falls back to a
// direct OS-level unmount so a stray mount can still be cleaned up.
func runUmount(mountpoint string) error {
	mpAbs, err := filepath.Abs(mountpoint)
	if err != nil {
		mpAbs = mountpoint
	}

	resp, err := daemonCall(daemonRequest{Cmd: "unmount", Mountpoint: mpAbs})
	switch {
	case errors.Is(err, errDaemonNotRunning):
		// No daemon: fall back to a direct OS unmount so a stray mount left by
		// a crashed daemon can still be cleaned up.
		return directUnmount(mpAbs)
	case err != nil:
		return fmt.Errorf("umount: %w", err)
	case resp.OK:
		fmt.Println(resp.Message)
		// The daemon may have only pruned a stale registry entry; if a real
		// mount is still lingering at the requested path or at a returned
		// registry mountpoint (e.g. caller passed the source path), clear it too
		// — quietly, and only when something is actually mounted there.
		cleanupTargets := []string{mpAbs}
		for _, m := range resp.Mounts {
			if m.Mountpoint != "" {
				cleanupTargets = append(cleanupTargets, m.Mountpoint)
			}
		}
		seen := map[string]bool{}
		for _, target := range cleanupTargets {
			if seen[target] {
				continue
			}
			seen[target] = true
			if isMountpoint(target) {
				if uerr := tryUnmount("diskutil", []string{"unmount", "force", target}, 5); uerr == nil {
					fmt.Printf("Also cleared a lingering mount at %s\n", target)
				}
			}
		}
		return nil
	default:
		// Daemon up but doesn't know this path and it isn't in the registry.
		// If something is nonetheless mounted there, clean it directly;
		// otherwise report the daemon's clean message.
		if isMountpoint(mpAbs) {
			return directUnmount(mpAbs)
		}
		return fmt.Errorf("umount: %s", resp.Error)
	}
}

// isMountpoint reports whether path is a mount point, by comparing its device
// number with its parent's — a cheap check that avoids running diskutil
// against a path where nothing is actually mounted.
func isMountpoint(path string) bool {
	var st, parent syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return false
	}
	if err := syscall.Lstat(filepath.Dir(path), &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}

// directUnmount performs an OS-level unmount without the daemon: macFUSE's
// native tools first, then force, then signals any pidfile owner and clears
// the mounts registry.
func directUnmount(mountpoint string) error {
	var errs []string

	if err := tryUnmount("diskutil", []string{"unmount", mountpoint}, 5); err != nil {
		errs = append(errs, fmt.Sprintf("diskutil unmount failed: %v", err))
		if err2 := tryUnmount("umount", []string{mountpoint}, 5); err2 != nil {
			errs = append(errs, fmt.Sprintf("umount failed: %v", err2))
			if err3 := tryUnmount("diskutil", []string{"unmount", "force", mountpoint}, 5); err3 != nil {
				errs = append(errs, fmt.Sprintf("diskutil unmount force failed: %v", err3))
			} else {
				errs = nil
			}
		} else {
			errs = nil
		}
	}

	if pid, pidErr := readPidfile(mountpoint); pidErr == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
	}
	_ = removePidfile(mountpoint)
	_ = config.RemoveMount(mountpoint)

	if len(errs) > 0 {
		return fmt.Errorf("umount %s:\n  %s", mountpoint, strings.Join(errs, "\n  "))
	}
	fmt.Printf("Unmounted %s\n", mountpoint)
	return nil
}

// tryUnmount runs an unmount command with a timeout, returning nil on success.
func tryUnmount(name string, args []string, timeoutSec int) error {
	cmd := exec.Command(name, args...)
	done := make(chan error, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				done <- fmt.Errorf("%s: %s", err, msg)
			} else {
				done <- err
			}
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(time.Duration(timeoutSec) * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("%s timed out after %ds", name, timeoutSec)
	}
}
