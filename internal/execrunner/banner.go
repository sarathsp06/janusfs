package execrunner

import (
	"fmt"
	"io"
	"path/filepath"
)

// WriteBanner prints the two-line "you are now inside the boundary" notice an
// operator sees when a session starts. Without it, `janusfs exec` is
// indistinguishable from running the command bare — the single most common
// source of "is it even on?" doubt. It names the protected tree, the network
// mode and where that mode came from, and the one command that previews the
// boundary in detail. Everything goes to w (stderr), never stdout, so piped
// output of the wrapped command is untouched.
func WriteBanner(w io.Writer, plan Plan, opts Options) {
	policy := plan.PolicyPath
	if rel, err := filepath.Rel(plan.Src, plan.PolicyPath); err == nil {
		policy = rel
	}
	fmt.Fprintf(w, "janusfs exec: boundary up — %s (policy %s)\n", plan.Src, policy)
	fmt.Fprintf(w, "  network: %s · preview with `janusfs exec --dry-run`\n", NetDescription(opts.DenyNetwork, opts.NetSource))
	if opts.Nested {
		fmt.Fprintln(w, "  nested: already inside a janusfs exec boundary; this adds a second layer over it")
	}
}

// NetDescription renders a network mode for humans: what it means and where
// it was decided, e.g. "none (loopback only; from .janusfs.yml exec.net)".
func NetDescription(denyNetwork bool, source string) string {
	mode := "host (not isolated; add --net=none to cut it)"
	if denyNetwork {
		mode = "none (loopback only)"
	}
	if source == "" {
		return mode
	}
	return fmt.Sprintf("%s · from %s", mode, source)
}
