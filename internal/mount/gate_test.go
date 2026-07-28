//go:build darwin || linux

package mount

import (
	"syscall"
	"testing"

	"github.com/sarathsp06/janusfs/internal/engine"
)

// TestGate pins the FR-8 behaviour matrix: how each operation class maps a
// path's Decision to an errno. This is the single source of truth the FUSE
// handlers consult, so the whole allow/mask/hide access rule is asserted here
// without a live mount.
func TestGate(t *testing.T) {
	const ok = syscall.Errno(0)
	cases := []struct {
		name     string
		class    gateClass
		decision engine.Decision
		want     syscall.Errno
	}{
		// Mutating ops (create/delete/rename/chmod/hardlink/xattr writes)
		// require ALLOWED — MASKED and HIDDEN both deny.
		{"mutate/allowed", denyNonAllowed, engine.Allowed, ok},
		{"mutate/masked", denyNonAllowed, engine.Masked, syscall.EACCES},
		{"mutate/hidden", denyNonAllowed, engine.Hidden, syscall.EACCES},

		// Read/traverse ops (opendir/readlink/xattr reads) and dir
		// create/remove pass ALLOWED and MASKED, denying only HIDDEN.
		{"read/allowed", denyHidden, engine.Allowed, ok},
		{"read/masked", denyHidden, engine.Masked, ok},
		{"read/hidden", denyHidden, engine.Hidden, syscall.EACCES},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gate(c.class, c.decision); got != c.want {
				t.Errorf("gate(%v, %v) = %v, want %v", c.class, c.decision, got, c.want)
			}
		})
	}
}
