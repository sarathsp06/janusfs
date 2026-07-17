package main

import (
	"fmt"
	"syscall"

	"github.com/sarathsp06/janusfs/internal/mount"
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

// runUmount implements FR-3: unmount at the OS level (via
// mount.Adapter.Unmount, which uses fuse.Unmount / diskutil fallback), and
// signal the owning process if discoverable via its pidfile so it can run
// its own clean-shutdown path (SPEC §15 step 11: flush history, zero
// caches, remove its own pidfile) rather than leaving that to us.
func runUmount(mountpoint string) error {
	adapter := &mount.Adapter{}
	unmountErr := adapter.Unmount(mountpoint)

	pid, pidErr := readPidfile(mountpoint)
	if pidErr == nil && pid > 0 {
		// Best-effort: the owning process may already be gone (e.g. it
		// exited when the OS-level unmount above completed), in which case
		// signaling fails harmlessly.
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	// Whether or not the owning process is still around to clean up after
	// itself, don't leave a stale pidfile behind.
	_ = removePidfile(mountpoint)

	if unmountErr != nil {
		return fmt.Errorf("umount: %w", unmountErr)
	}
	return nil
}
