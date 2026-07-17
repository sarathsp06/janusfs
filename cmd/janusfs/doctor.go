package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDoctorCmd is a placeholder for FR-29's runtime health/diagnostics
// command, which depends on internal/health, internal/obs, and
// internal/history (Phase 4/5) — none exist yet. Stubbed with a clear,
// honest message rather than silently doing nothing.
func newDoctorCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report FUSE-T status, active mounts, and health (not yet implemented)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("doctor: not yet implemented (FR-29, planned for Phase 5; internal/health/obs/history land first)")
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "full compiled rule dump (not yet implemented)")
	return cmd
}
