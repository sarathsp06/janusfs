package main

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newShellCmd is `janusfs exec -- $SHELL` with a name people will find: the
// first thing most operators want is to poke around inside the boundary and
// see for themselves that `cat .env` prints stars.
func newShellCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell [--net=host|none] [--quiet] [--dry-run]",
		Short: "Open an interactive shell inside the secret-masked view of the current project",
		Long: "Opens your login shell ($SHELL, falling back to /bin/sh) inside the same\n" +
			"kernel-enforced boundary `janusfs exec` uses: every file in the project\n" +
			"reads Allowed, Masked, or Hidden exactly as an agent would see it. Good for\n" +
			"checking a policy by hand (`cat .env`, `ls`, `git status`) before pointing an\n" +
			"agent at it. The shell's environment carries JANUSFS_EXEC=1, so a prompt can\n" +
			"show it; for example in bash: PS1='${JANUSFS_EXEC:+[janusfs] }'\"$PS1\".\n\n" +
			"Accepts the same flags as exec (--net, --quiet, --dry-run). Linux only.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			inv, err := parseExecOwnArgs(args)
			if err != nil {
				return rewordForShell(err)
			}
			return launchInBoundary(cmd, inv, []string{loginShell()})
		},
	}
	return cmd
}

// loginShell picks the shell to run: the user's own ($SHELL), else /bin/sh,
// which every Linux host has.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// rewordForShell swaps exec's flag-error wording for shell's, since shell
// takes no command for a flag to be before.
func rewordForShell(err error) error {
	msg := strings.ReplaceAll(err.Error(), "exec: ", "shell: ")
	msg = strings.ReplaceAll(msg, " before the command", "")
	return &shellErr{msg}
}

type shellErr struct{ msg string }

func (e *shellErr) Error() string { return e.msg }
