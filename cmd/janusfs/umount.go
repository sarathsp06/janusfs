package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newUmountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "umount <mountpoint>",
		Short: "Unmount an active JanusFS mount",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUmount(args[0])
		},
	}
}

// runUmount implements FR-3: unmount at the OS level by running
// macFUSE's native unmount command first, with fallbacks, and signal
// the owning process if discoverable via its pidfile.
func runUmount(mountpoint string) error {
	var errs []string

	// Step 1: Try osxfuse unmount (macFUSE's native tool).
	if err := tryUnmount("diskutil", []string{"unmount", mountpoint}, 5); err != nil {
		errs = append(errs, fmt.Sprintf("diskutil unmount failed: %v", err))
		// Fall back to umount.
		if err2 := tryUnmount("umount", []string{mountpoint}, 5); err2 != nil {
			errs = append(errs, fmt.Sprintf("umount failed: %v", err2))
			// Last resort: force unmount. A held reference (Finder, a shell
			// cd'd into the mount) makes both prior attempts fail with
			// "resource busy" every time, so this step actually has to run,
			// not just be printed as advice.
			if err3 := tryUnmount("diskutil", []string{"unmount", "force", mountpoint}, 5); err3 != nil {
				errs = append(errs, fmt.Sprintf("diskutil unmount force failed: %v", err3))
			} else {
				errs = nil // force unmount succeeded
			}
		} else {
			errs = nil // umount succeeded
		}
	}

	// Step 2: Signal owning process so it can flush history, zero caches, etc.
	pid, pidErr := readPidfile(mountpoint)
	if pidErr == nil && pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		// Give it a moment to clean up.
		time.Sleep(200 * time.Millisecond)
	}
	// Don't leave a stale pidfile behind.
	_ = removePidfile(mountpoint)

	if len(errs) > 0 {
		return fmt.Errorf("umount %s:\n  %s", mountpoint,
			strings.Join(errs, "\n  "))
	}
	fmt.Printf("Unmounted %s\n", mountpoint)
	return nil
}

// tryUnmount runs a command to unmount the given path, with a timeout.
// Returns nil on success, or an error describing what went wrong.
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
