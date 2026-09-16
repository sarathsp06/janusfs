//go:build !linux

package backing

import "golang.org/x/sys/unix"

// rootOpenFlags off Linux: no O_PATH, so O_RDONLY|O_DIRECTORY is the openat(2)
// base instead. JanusFS is only supported on Linux; this shim exists so the
// package still compiles on developer machines (notably darwin), not because
// mounting works here.
const rootOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
