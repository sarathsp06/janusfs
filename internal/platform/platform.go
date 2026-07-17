// Package platform is the OS-abstraction seam described in SPEC.md §5: it
// exposes constructors for the mount and watch subsystems so the rest of
// the codebase never branches on GOOS. Only a darwin implementation exists
// today (SPEC.md scope: macOS only); a linux implementation is deferred
// behind this same seam per SPEC.md Non-Goals.
package platform

import "context"

// Mounter mounts a filesystem implementation at a mountpoint and blocks
// until it is unmounted or ctx is canceled. Implements the FUSE adapter
// side of SPEC.md §6.
type Mounter interface {
	// Mount blocks until the mount is unmounted (via Unmount, ctx
	// cancellation, or an external unmount) and returns any resulting
	// error.
	Mount(ctx context.Context, src, mountpoint string) error
	// Unmount requests a clean unmount (FR-2/FR-3).
	Unmount(mountpoint string) error
}
