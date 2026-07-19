package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sarathsp06/janusfs/internal/health"
	"github.com/spf13/cobra"
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

			report := health.Run(pidfileDir)

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				enc.Encode(report)
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
	fmt.Printf("JanusFS Doctor — %s\n", r.Version)
	fmt.Println()

	// macFUSE status.
	fmt.Print("macFUSE: ")
	if r.MacFUSE.Installed {
		fmt.Print("installed")
		if r.MacFUSE.Loaded {
			fmt.Print(", loaded")
		} else {
			fmt.Print(", NOT loaded (run `sudo kextload` or approve in System Settings)")
		}
		if r.MacFUSE.Version != "" {
			fmt.Printf(" (version %s)", r.MacFUSE.Version)
		}
		fmt.Println()
	} else {
		fmt.Println("NOT installed (install with `brew install --cask macfuse`)")
	}

	// Runtime.
	fmt.Printf("Runtime: %s %s/%s, %d CPU(s), %d goroutine(s)\n",
		r.Runtime.GoVersion, r.Runtime.OS, r.Runtime.Arch,
		r.Runtime.NumCPU, r.Runtime.NumGoroutine)

	// Mounts.
	fmt.Printf("Active mounts: %d\n", len(r.Mounts))
	for _, m := range r.Mounts {
		status := "alive"
		if !m.Alive {
			status = "STALE"
		}
		fmt.Printf("  %s (pid %d) — %s\n", m.Mountpoint, m.PID, status)
	}

	// Warnings.
	if len(r.Warnings) > 0 {
		fmt.Println()
		fmt.Println("Warnings:")
		for _, w := range r.Warnings {
			fmt.Printf("  * %s\n", w)
		}
	}
}
