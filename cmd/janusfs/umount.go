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
	case err == nil && resp.OK:
		fmt.Println(resp.Message)
		return nil
	case errors.Is(err, errDaemonNotRunning):
		// No daemon — clean up directly below.
	case err != nil:
		return fmt.Errorf("umount: %w", err)
	default:
		// Daemon running but doesn't own this mountpoint; try direct cleanup.
	}

	return directUnmount(mpAbs)
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
