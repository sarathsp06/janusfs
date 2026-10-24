//go:build !linux

// On Linux, janusfs exec runs the target inside a private mount (and optional
// user/network) namespace so the kernel presents the sanitized view at the
// project's own path and confines the process to it (see runner_linux.go).
// That boundary is a Linux kernel feature; no other OS has an equivalent, so
// there is nothing to enforce here. Rather than pretend, this stub refuses.
// Discover and Inspect (the --dry-run path) still work everywhere, so a
// policy can be previewed on a laptop before the agent runs on a Linux box.
package execrunner

import (
	"context"
	"errors"
)

// ErrUnsupportedOS is returned by Run on any platform other than Linux.
var ErrUnsupportedOS = errors.New("janusfs exec requires Linux: kernel-enforced isolation (mount/user/network namespaces) is a Linux feature and has no equivalent on this OS; run inside a Linux container or host (`janusfs exec --dry-run` still previews the boundary here)")

// Run refuses on non-Linux platforms. The signature matches runner_linux.go so
// callers (cmd/janusfs) compile unchanged; enforcement simply does not exist
// off Linux.
func Run(_ context.Context, _ Plan, _ []string, _ Options) (int, error) {
	return 1, ErrUnsupportedOS
}
