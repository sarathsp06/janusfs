package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/execrunner"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run a command against a sanitized view of the current source tree",
		Long: "Runs a command against a sanitized view of the current source tree. Requires\n" +
			"a running daemon (`janusfs daemon`) and refuses to run if no .janusfs.yml\n" +
			"policy exists anywhere in the tree, rather than guessing which\n" +
			"directory to protect.\n\n" +
			"Linux: real, kernel-enforced confinement. Runs inside a private mount\n" +
			"namespace where the filtered view replaces the source at its own path — no\n" +
			"path rewriting, because the kernel makes the two paths the same path. The\n" +
			"namespaced child runs under CLONE_NEWUSER and sees itself as uid 0; some\n" +
			"tools behave differently as root.\n\n" +
			"macOS: advisory only. Sets the child's working directory to a disjoint\n" +
			"sanitized mount and scrubs JANUSFS_* env vars. This does not stop the\n" +
			"child reaching the real source path directly by any other means — for\n" +
			"kernel-enforced confinement, run the agent in a Linux container and use\n" +
			"janusfs exec inside it. Stdout/stderr are passed through byte-faithfully\n" +
			"so interactive tools keep their terminal behavior.",
		// DisableFlagParsing: everything after "exec" other than a leading -h/--help
		// is captured as the command to run or its arguments, never parsed as a
		// flag of this command — but that also means cobra's own --help
		// interception never runs, so --help is handled explicitly.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}

			// Find "--" to separate the command we want to run from anything
			// before it. exec has no flags of its own, so anything before
			// "--" is a mistake worth naming rather than silently running.
			sepIdx := -1
			for i, arg := range args {
				if arg == "--" {
					sepIdx = i
					break
				}
			}

			var ownArgs, targetArgs []string
			if sepIdx == -1 {
				targetArgs = args
			} else {
				ownArgs = args[:sepIdx]
				targetArgs = args[sepIdx+1:]
			}

			if len(ownArgs) > 0 {
				return fmt.Errorf("exec: unrecognized flag %q before \"--\"", ownArgs[0])
			}

			if len(targetArgs) == 0 {
				return errors.New("exec: command to run is required (use: janusfs exec -- <command> [args...])")
			}

			exitCode, err := execrunner.Run(cmd.Context(), targetArgs)
			if err != nil {
				// Fail closed: emit a one-line cause/remedy to stderr and exit.
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(exitCode)
			}

			os.Exit(exitCode)
			return nil
		},
	}
	return cmd
}
