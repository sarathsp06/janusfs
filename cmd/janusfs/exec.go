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
		Use:   "exec [--sandbox] -- <command> [args...]",
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
			"macOS: advisory only by default. Sets the child's working directory to a\n" +
			"disjoint sanitized mount, scrubs JANUSFS_* env vars, and rewrites\n" +
			"source-path arguments to the mountpoint as a best-effort compatibility\n" +
			"shim. This does not stop the child reaching the real source path directly\n" +
			"by any other means. Stdout/stderr are passed through byte-faithfully so\n" +
			"interactive tools keep their terminal behavior.\n\n" +
			"--sandbox (macOS only): confines the child process tree with Seatbelt\n" +
			"(sandbox-exec), denying read and write of the real source path while the\n" +
			"mountpoint stays usable. A real, kernel-enforced deny boundary for Hidden;\n" +
			"Masked files are still served by FUSE through the mountpoint, unchanged.\n" +
			"Fails closed (non-zero exit, child never runs) if sandbox-exec is\n" +
			"unavailable. No effect on Linux, where the mount namespace already\n" +
			"enforces this. Not yet validated against signed/Electron app-bundle\n" +
			"harnesses — intended for plain-CLI agents.",
		// DisableFlagParsing: everything after "exec" other than a leading -h/--help
		// is captured as the command to run or its arguments, never parsed as a
		// flag of this command — but that also means cobra's own --help
		// interception (and its own flag parsing) never runs, so --sandbox is
		// pulled out of the pre-"--" args by hand below, and --help is handled
		// explicitly.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}

			// Find "--" to separate the command we want to run from our own
			// flags (only --sandbox exists today, checked against everything
			// before "--").
			sepIdx := -1
			for i, arg := range args {
				if arg == "--" {
					sepIdx = i
					break
				}
			}

			var ownArgs, targetArgs []string
			if sepIdx == -1 {
				// No "--": treat all args as the command, same as before
				// --sandbox existed — there is nothing to scan for a flag.
				targetArgs = args
			} else {
				ownArgs = args[:sepIdx]
				targetArgs = args[sepIdx+1:]
			}

			sandbox := false
			for _, a := range ownArgs {
				switch a {
				case "--sandbox":
					sandbox = true
				default:
					return fmt.Errorf("exec: unrecognized flag %q before \"--\"", a)
				}
			}

			if len(targetArgs) == 0 {
				return errors.New("exec: command to run is required (use: janusfs exec -- <command> [args...])")
			}

			exitCode, err := execrunner.Run(cmd.Context(), targetArgs, sandbox)
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
