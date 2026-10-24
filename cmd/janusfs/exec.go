package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/execrunner"
)

func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [--net=host|none] -- <command> [args...]",
		Short: "Run a command against a sanitized view of the current source tree",
		Long: "Runs a command against a sanitized view of the current source tree. Requires\n" +
			"a running daemon (`janusfs daemon`) and refuses to run if no .janusfs.yml\n" +
			"policy exists anywhere in the tree, rather than guessing which\n" +
			"directory to protect.\n\n" +
			"JanusFS runs on Linux only: real, kernel-enforced confinement. Runs\n" +
			"inside a private mount namespace where the filtered view replaces the\n" +
			"source at its own path — no path rewriting, because the kernel makes the\n" +
			"two paths the same path. The namespaced child runs under CLONE_NEWUSER\n" +
			"and sees itself as uid 0; some tools behave differently as root.\n" +
			"Stdout/stderr are passed through byte-faithfully so interactive tools\n" +
			"keep their terminal behavior. On non-Linux hosts exec refuses to run;\n" +
			"to use JanusFS on a Mac, run the agent in a Linux container/VM.\n\n" +
			"--net controls network access for the command (default host):\n" +
			"  host  share the host network (no isolation)\n" +
			"  none  no network at all — the command runs with only a loopback\n" +
			"        interface and cannot reach any external host. Kernel-enforced.\n" +
			"The default when --net is omitted comes from JANUSFS_EXEC_NET, then\n" +
			"exec_net in ~/.janusfs/settings.json, then host.",
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

			cfg := config.Default()
			if err := config.ApplyFile(&cfg); err != nil {
				return err
			}
			if err := config.ApplyEnv(&cfg); err != nil {
				return err
			}

			denyNetwork, err := parseExecOwnArgs(ownArgs, cfg.ExecNet)
			if err != nil {
				return err
			}

			if len(targetArgs) == 0 {
				return errors.New("exec: command to run is required (use: janusfs exec -- <command> [args...])")
			}

			exitCode, err := execrunner.Run(cmd.Context(), targetArgs, execrunner.Options{DenyNetwork: denyNetwork})
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

// parseExecOwnArgs interprets the arguments before "--" on a `janusfs exec`
// line. exec disables cobra flag parsing so it can forward the target command
// verbatim, so its own flags are parsed here by hand. --net is the only one;
// anything else is a mistake worth naming rather than silently forwarding.
// defaultMode is the config/env-resolved network mode used when --net is
// omitted; an explicit --net overrides it. The final mode is validated here,
// so a bad value from either source is caught the same way.
func parseExecOwnArgs(ownArgs []string, defaultMode string) (denyNetwork bool, err error) {
	netMode := defaultMode
	for _, arg := range ownArgs {
		if !strings.HasPrefix(arg, "--net=") {
			return false, fmt.Errorf("exec: unrecognized flag %q before \"--\"", arg)
		}
		netMode = strings.TrimPrefix(arg, "--net=")
	}
	switch netMode {
	case "host":
		return false, nil
	case "none":
		return true, nil
	default:
		return false, fmt.Errorf("exec: invalid network mode %q (want host or none; set via --net, JANUSFS_EXEC_NET, or exec_net in settings.json)", netMode)
	}
}
