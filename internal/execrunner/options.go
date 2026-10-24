package execrunner

// Options configures a single `janusfs exec` run. Enforcement is Linux-only;
// on other platforms Run refuses rather than honoring any field advisorily.
type Options struct {
	// DenyNetwork runs the target command with no network access: on Linux the
	// child is placed in its own network namespace whose only interface is
	// loopback, so no packet can reach any external host. This is the
	// deny-all equivalent of a container's `--network none`, and like the rest
	// of the Linux exec path it is kernel-enforced.
	// It is Linux-only, like the whole exec path; off Linux, Run refuses before
	// any of these fields would be consulted.
	DenyNetwork bool

	// NetSource names where DenyNetwork's value came from ("--net flag",
	// ".janusfs.yml exec.net", "settings/env", "default"); display only, for
	// the start banner.
	NetSource string

	// Nested is true when this exec was launched from inside another
	// `janusfs exec` session (JANUSFS_EXEC=1 was in the environment). Nesting
	// works — the inner namespace mounts over the outer view, and an inner
	// --net=none adds the network cut to a session that lacked it — but it
	// is worth saying out loud, as tmux does with $TMUX. Display only.
	Nested bool

	// Quiet suppresses the operator-facing start banner and the end-of-session
	// summary on stderr. Security warnings (git staging hazards) still print:
	// quiet is about noise, not about hiding a hazard.
	Quiet bool
}
