//go:build linux

package mount

import "github.com/hanwen/go-fuse/v2/fs"

// applyPlatformOptions is a no-op on Linux: the shared mount options in
// mount.go are all Linux FUSE needs. The macFUSE-specific options live in
// mount_darwin.go.
func applyPlatformOptions(_ *fs.Options) {}
