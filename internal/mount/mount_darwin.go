//go:build darwin

package mount

import "github.com/hanwen/go-fuse/v2/fs"

// applyPlatformOptions applies the macFUSE-specific mount options. It is the
// entire platform difference in the adapter; the shared Mount lifecycle lives
// in mount.go. (This adapter runs over macFUSE, chosen over an earlier
// jacobsa/fuse + FUSE-T stack whose NFS transport made mounting unreliable.)
func applyPlatformOptions(opts *fs.Options) {
	// NullPermissions: let the kernel handle permission checks against the
	// reported mode bits rather than having go-fuse do it — avoids spurious
	// EACCES for root vs. user ownership mismatches on the loopback.
	opts.NullPermissions = true

	// macFUSE holds a volume busy by default: Spotlight (mdworker) indexes it
	// and Finder browses it, so a graceful unmount fails with EBUSY forever.
	// nobrowse keeps it out of Finder + Spotlight; noappledouble stops the
	// ._* / .DS_Store writes Finder would otherwise make. Both are the
	// standard cure for "macFUSE won't unmount".
	opts.Options = append(opts.Options, "nobrowse", "noappledouble")
}
