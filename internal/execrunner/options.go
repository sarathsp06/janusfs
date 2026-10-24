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
}
