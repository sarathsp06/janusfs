package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/sarathsp06/janusfs/internal/execrunner"
	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run a command against a sanitized view of the current source tree",
		Long: "Runs a command against a sanitized view of the current source tree. Requires\n" +
			"a running daemon (`janusfs daemon`) and refuses to run if no .janusignore/\n" +
			".janusmask policy exists anywhere in the tree, rather than guessing which\n" +
			"directory to protect.\n\n" +
			"Linux: real, kernel-enforced confinement. Runs inside a private mount\n" +
			"namespace where the filtered view replaces the source at its own path — no\n" +
			"path rewriting, because the kernel makes the two paths the same path.\n\n" +
			"macOS: advisory only. Sets the child's working directory to a disjoint\n" +
			"sanitized mount, scrubs JANUSFS_* env vars, and rewrites the mountpoint back\n" +
			"to the source path in arguments and in stdout/stderr as a best-effort\n" +
			"compatibility shim. This does not stop the child reaching the real source\n" +
			"path directly by any other means, and output containing the mountpoint\n" +
			"string is rewritten, not a byte-faithful reproduction.",
		// DisableFlagParsing: everything after "exec" other than a leading -h/--help
		// is captured as the command to run or its arguments, never parsed as a
		// flag of this command — but that also means cobra's own --help
		// interception never runs, so it's handled explicitly below.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}

			// Find "--" to separate the command we want to run.
			var targetArgs []string
			for i, arg := range args {
				if arg == "--" {
					targetArgs = args[i+1:]
					break
				}
			}

			// If no "--" was provided, use all arguments.
			if len(targetArgs) == 0 {
				if len(args) > 0 && args[0] != "--" {
					targetArgs = args
				} else {
					return errors.New("exec: command to run is required (use: janusfs exec -- <command> [args...])")
				}
			}

			exitCode, err := execrunner.Run(cmd.Context(), targetArgs)
			if err != nil {
				// According to FR-Exec-1 / FR-Exec-2 / Fail-closed:
				// Emit a one-line cause/remedy to stderr and exit.
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(exitCode)
			}

			os.Exit(exitCode)
			return nil
		},
	}
	return cmd
}
