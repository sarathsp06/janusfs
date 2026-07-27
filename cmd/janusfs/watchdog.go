package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newWatchdogCmd() *cobra.Command {
	var pid int
	var mountpoint string

	cmd := &cobra.Command{
		Use:    "watchdog",
		Short:  "Internal supervisor watchdog process for macOS mounts",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pid <= 0 || mountpoint == "" {
				return fmt.Errorf("pid and mount are required")
			}
			runWatchdog(cmd.Context(), pid, mountpoint)
			return nil
		},
	}
	cmd.Flags().IntVar(&pid, "pid", 0, "PID of the daemon to monitor")
	cmd.Flags().StringVar(&mountpoint, "mount", "", "mountpoint to unmount if daemon dies")
	return cmd
}

var osExit = os.Exit

func runWatchdog(ctx context.Context, pid int, mountpoint string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check if daemon pid is still alive
			err := syscall.Kill(pid, 0)
			if err != nil {
				// Daemon has died! Force unmount and exit (PRP §4.2)
				_ = unmountKernel(mountpoint, true)
				osExit(0)
				return
			}
		}
	}
}
