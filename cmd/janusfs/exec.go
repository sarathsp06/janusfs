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
		Short: "Execute a command transparently inside a sanitized JanusFS mount",
		Long: "Executes a command inside a sanitized JanusFS mount of the current source tree.\n" +
			"Automatically provisions a background mount if none exists, hijacks the working directory,\n" +
			"translates path arguments bidirectionally, scrubs JanusFS env vars, and forwards signals.",
		DisableFlagParsing: true, // we want to capture everything after "exec" as the command or arguments
		RunE: func(cmd *cobra.Command, args []string) error {
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
