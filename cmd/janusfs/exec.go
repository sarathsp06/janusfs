package main

import (
	"encoding/json"
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
		Use:   "exec [--net=host|none] [--quiet] [--dry-run [--json]] -- <command> [args...]",
		Short: "Run a command inside a kernel-enforced, secret-masked view of the current project",
		Long: "Runs a command inside a filtered view of the project tree that contains the\n" +
			"current directory. The nearest ancestor-or-self directory carrying a\n" +
			".janusfs.yml is the protected tree; exec refuses to run if there is none,\n" +
			"rather than guessing which directory to protect. No daemon is needed.\n\n" +
			"JanusFS runs on Linux only: real, kernel-enforced confinement. The command\n" +
			"runs inside a private mount namespace where the filtered view replaces the\n" +
			"source at its own path — no path rewriting, because the kernel makes the\n" +
			"two paths the same path. Everything the command spawns inherits the view.\n" +
			"The namespaced child runs under CLONE_NEWUSER and sees itself as uid 0;\n" +
			"some tools behave differently as root. Stdout/stderr are passed through\n" +
			"byte-faithfully so interactive tools keep their terminal behavior. The\n" +
			"child's environment carries JANUSFS_EXEC=1 so prompts and scripts can tell\n" +
			"they are inside the boundary. On non-Linux hosts exec refuses to run; to\n" +
			"use JanusFS on a Mac, run the agent in a Linux container/VM.\n\n" +
			"Flags (before \"--\"):\n" +
			"  --net=host|none  network access for the command. host shares the host\n" +
			"                   network (no isolation); none gives the command only a\n" +
			"                   loopback interface and no route out — kernel-enforced.\n" +
			"                   When omitted: `exec.net` in the project's .janusfs.yml\n" +
			"                   (which can only tighten), then JANUSFS_EXEC_NET, then\n" +
			"                   exec_net in ~/.janusfs/settings.json, then host.\n" +
			"  --quiet, -q      no start banner or end-of-session summary on stderr.\n" +
			"                   Security warnings still print.\n" +
			"  --dry-run        show what exec would do — the protected tree, the\n" +
			"                   network mode and where it came from, whether this host\n" +
			"                   can enforce, and every file that would be hidden or\n" +
			"                   masked — and exit without running anything. Works on\n" +
			"                   every OS. --json makes it machine-readable.\n\n" +
			"For an interactive shell inside the boundary, see `janusfs shell`.",
		// DisableFlagParsing: everything after "exec" other than a leading -h/--help
		// is captured as the command to run or its arguments, never parsed as a
		// flag of this command — but that also means cobra's own --help
		// interception never runs, so --help is handled explicitly.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}

			ownArgs, targetArgs := splitExecArgs(args)
			inv, err := parseExecOwnArgs(ownArgs)
			if err != nil {
				return err
			}
			if len(targetArgs) == 0 && !inv.dryRun {
				return errors.New("exec: command to run is required (use: janusfs exec -- <command> [args...]; or `janusfs shell` for an interactive shell inside the boundary)")
			}
			return launchInBoundary(cmd, inv, targetArgs)
		},
	}
	return cmd
}

// splitExecArgs separates exec's own flags from the command to run. A "--"
// is the explicit boundary and always wins; without one, leading arguments
// that are exec's own flags are taken as such and the first argument that is
// not one starts the command — so `janusfs exec --net=none aider` and
// `janusfs exec --dry-run` both read the way they look. Anything after the
// command starts belongs to the command, flags included.
func splitExecArgs(args []string) (ownArgs, targetArgs []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	i := 0
	for i < len(args) && isExecOwnFlag(args[i]) {
		i++
	}
	return args[:i], args[i:]
}

func isExecOwnFlag(arg string) bool {
	switch {
	case strings.HasPrefix(arg, "--net="), arg == "--quiet", arg == "-q", arg == "--dry-run", arg == "--json":
		return true
	}
	return false
}

// execInvocation is exec's own flags, parsed by hand (see parseExecOwnArgs).
type execInvocation struct {
	netFlag string // "" when --net was not given
	quiet   bool
	dryRun  bool
	jsonOut bool
}

// parseExecOwnArgs interprets the arguments before "--" on a `janusfs exec`
// line. exec disables cobra flag parsing so it can forward the target command
// verbatim, so its own flags are parsed here by hand; anything unknown is a
// mistake worth naming rather than silently forwarding. Validation of the
// network mode happens later, in resolveNetMode, once the repo policy and
// settings are known, so that a bad value from any source fails the same way.
func parseExecOwnArgs(ownArgs []string) (execInvocation, error) {
	var inv execInvocation
	for _, arg := range ownArgs {
		switch {
		case strings.HasPrefix(arg, "--net="):
			inv.netFlag = strings.TrimPrefix(arg, "--net=")
		case arg == "--quiet" || arg == "-q":
			inv.quiet = true
		case arg == "--dry-run":
			inv.dryRun = true
		case arg == "--json":
			inv.jsonOut = true
		default:
			return inv, fmt.Errorf("exec: unrecognized flag %q before the command (known: --net=host|none, --quiet, --dry-run, --json)", arg)
		}
	}
	if inv.jsonOut && !inv.dryRun {
		return inv, errors.New("exec: --json only applies with --dry-run")
	}
	return inv, nil
}

// resolveNetMode decides the network mode for a run and names its source.
// Lowest to highest: the machine-wide default/settings/env (cfgNet, already
// merged by internal/config), then the repository's own `exec.net` — which
// can only tighten, so a repo's "host" never loosens an operator's "none" —
// then an explicit --net flag, because the operator at the keyboard is
// trusted. A flag that loosens the repo's request is honoured but noted, so
// nobody discovers weeks later that a sandbox they thought was offline was
// not. Any invalid value, from any source, is refused here, in one place.
func resolveNetMode(flag, policyNet, cfgNet string) (denyNetwork bool, source, note string, err error) {
	mode, source := cfgNet, "settings/env"
	if cfgNet == config.DefaultExecNet {
		source = "default"
	}
	if policyNet == "none" {
		mode, source = "none", ".janusfs.yml exec.net"
	}
	if flag != "" {
		mode, source = flag, "--net flag"
		if policyNet == "none" && flag == "host" {
			note = "exec: note: this project's .janusfs.yml asks for exec.net: none; --net=host overrides it for this run"
		}
	}
	switch mode {
	case "host":
		return false, source, note, nil
	case "none":
		return true, source, note, nil
	default:
		return false, "", "", fmt.Errorf("exec: invalid network mode %q (want host or none; set via --net, exec.net in .janusfs.yml, JANUSFS_EXEC_NET, or exec_net in settings.json)", mode)
	}
}

// launchInBoundary is the shared tail of `exec` and `shell`: discover the
// plan, resolve the network mode, then either report (--dry-run) or run.
func launchInBoundary(cmd *cobra.Command, inv execInvocation, targetArgs []string) error {
	cfg := config.Default()
	if err := config.ApplyFile(&cfg); err != nil {
		return err
	}
	if err := config.ApplyEnv(&cfg); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("exec: failed to get current working directory: %w", err)
	}
	plan, err := execrunner.Discover(cwd)
	if err != nil {
		return err
	}
	denyNetwork, netSource, note, err := resolveNetMode(inv.netFlag, plan.ExecPolicy.Net, cfg.ExecNet)
	if err != nil {
		return err
	}
	netMode := "host"
	if denyNetwork {
		netMode = "none"
	}

	if inv.dryRun {
		ins, err := execrunner.Inspect(plan, netMode, netSource)
		if err != nil {
			return fmt.Errorf("exec --dry-run: %w", err)
		}
		if inv.jsonOut {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(ins)
		}
		printDryRun(cmd.OutOrStdout(), ins, note)
		return nil
	}

	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	exitCode, err := execrunner.Run(cmd.Context(), plan, targetArgs, execrunner.Options{
		DenyNetwork: denyNetwork,
		NetSource:   netSource,
		Quiet:       inv.quiet,
	})
	if err != nil {
		// Fail closed: emit a one-line cause/remedy to stderr and exit.
		fmt.Fprintln(os.Stderr, err.Error())
	}
	os.Exit(exitCode)
	return nil
}
