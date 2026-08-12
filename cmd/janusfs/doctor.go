package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/health"
)

func newDoctorCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report macFUSE status, active mounts, and runtime health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("doctor: %w", err)
			}
			pidfileDir := filepath.Join(home, ".janusfs", "run")
			watchdogPidfile := filepath.Join(home, ".janusfs", "watchdog.pid")

			report := health.Run(pidfileDir, watchdogPidfile)

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(report)
				return nil
			}

			printDoctorReport(report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func printDoctorReport(r *health.Report) {
	fmt.Printf("%s %s\n\n", cBold("JanusFS Doctor —"), r.Version)

	// FUSE/macFUSE status.
	fuseLabel := "FUSE"
	if r.Runtime.OS == "darwin" {
		fuseLabel = "macFUSE"
	}
	switch {
	case r.MacFUSE.Installed && r.MacFUSE.Loaded:
		msg := "installed, loaded"
		if r.MacFUSE.Version != "" {
			msg += fmt.Sprintf(" (version %s)", r.MacFUSE.Version)
		}
		fmt.Printf("%s %s: %s\n", symGood(), fuseLabel, msg)
	case r.MacFUSE.Installed:
		hint := ""
		if r.Runtime.OS == "darwin" {
			hint = " (run `sudo kextload` or approve in System Settings)"
		}
		fmt.Printf("%s %s: installed, %s%s\n", symWarn(), fuseLabel, cWarn("NOT loaded"), hint)
	default:
		hint := "install with `apt-get install fuse3` or your package manager"
		if r.Runtime.OS == "darwin" {
			hint = "install with `brew install --cask macfuse`"
		}
		fmt.Printf("%s %s: %s (%s)\n", symBad(), fuseLabel, cBad("NOT installed"), hint)
	}

	// Runtime.
	fmt.Printf("%s Runtime: %s %s/%s, %d CPU(s), %d goroutine(s)\n", symGood(),
		r.Runtime.GoVersion, r.Runtime.OS, r.Runtime.Arch,
		r.Runtime.NumCPU, r.Runtime.NumGoroutine)

	// Mounts.
	fmt.Printf("  Active mounts: %d\n", len(r.Mounts))
	for _, m := range r.Mounts {
		sym, status := symGood(), cGood("alive")
		if !m.Alive {
			sym, status = symBad(), cBad("STALE")
		}
		name := m.Mountpoint
		if !m.MountpointKnown {
			// m.Mountpoint here is a SHA-256 hash of the real path (from an
			// older pidfile predating mountpoint recording), never a path —
			// say so rather than printing a hash as if it were actionable.
			name = fmt.Sprintf("<mountpoint unknown, pidfile hash %s>", m.Mountpoint)
		}
		fmt.Printf("  %s %s %s — %s\n", sym, name, cDim(fmt.Sprintf("(pid %d)", m.PID)), status)
	}

	// Watchdog.
	switch {
	case !r.Watchdog.Present:
		fmt.Printf("%s Watchdog: not running %s\n", symWarn(),
			cDim("(crash recovery is manual — see `janusfs umount` if a mount hangs)"))
	case r.Watchdog.Alive:
		fmt.Printf("%s Watchdog: running %s\n", symGood(), cDim(fmt.Sprintf("(pid %d)", r.Watchdog.PID)))
	default:
		fmt.Printf("%s Watchdog: %s %s\n", symBad(), cBad("STALE"),
			cDim(fmt.Sprintf("(pid %d recorded but not alive)", r.Watchdog.PID)))
	}

	// Warnings.
	if len(r.Warnings) > 0 {
		fmt.Printf("\n%s\n", cWarn(cBold("Warnings:")))
		for _, w := range r.Warnings {
			fmt.Printf("  %s %s\n", symWarn(), w)
		}
	}
}
