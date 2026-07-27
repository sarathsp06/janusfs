// Command janusfs is the JanusFS CLI entrypoint.
//
// Subcommand dispatch, flag registration, and help/usage text are handled by
// spf13/cobra; this package wires cobra
// commands to internal/config, internal/logging, internal/apperrors, and
// internal/mount, and contains no decision, redaction, or SQL logic of its own:
// the thin-adapter rule applies to the CLI layer too, not just internal/mount
// and internal/api.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Build-time metadata injected via -ldflags by the Makefile / goreleaser.
// The zero values ("dev") are what a plain `go build ./cmd/janusfs` will
// leave in place; a release build overrides them.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	root := newRootCmd()
	// No raw Go error or stack trace reaches the user. cobra prints its
	// own usage+error by default; we silence both and print a single
	// one-line "cause" ourselves (SilenceErrors), keeping usage text
	// available only via --help (SilenceUsage) rather than dumped on every
	// error.
	root.SilenceErrors = true
	root.SilenceUsage = true

	if err := root.Execute(); err != nil {
		// An empty error message means the command already printed its own
		// user-facing report (e.g. `check`'s findings) and just needs the
		// non-zero exit code, not a redundant "janusfs: " line.
		if err.Error() != "" {
			fmt.Fprintln(os.Stderr, "janusfs: "+err.Error())
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "janusfs",
		Short:         "A sanitized filesystem view for AI agents",
		Long:          "JanusFS mounts a sanitized view of a directory: every file is Allowed, Masked, or Hidden, per .janusignore/.janusmask rules.",
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("janusfs {{.Version}}\n")
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newMountCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newUmountCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newPathsCmd())
	root.AddCommand(newPathCmd())
	root.AddCommand(newCheckCmd())
	root.AddCommand(newExplainCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newWatchdogCmd())
	registerPlatformCommands(root)
	return root
}
