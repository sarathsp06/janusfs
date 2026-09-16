//go:build darwin || linux

package mount

import "github.com/hanwen/go-fuse/v2/fs"

// applyPlatformOptions is a no-op: Linux FUSE needs only the shared options set
// in mount.go. The go-fuse adapter still compiles on non-Linux dev machines so
// the unit suite runs there, but mounts are only supported on Linux (startMount
// refuses elsewhere), so there are no per-platform mount options to apply.
func applyPlatformOptions(_ *fs.Options) {}
