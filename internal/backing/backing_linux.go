//go:build linux

package backing

import "golang.org/x/sys/unix"

// rootOpenFlags on Linux uses O_PATH: a lightweight descriptor that performs
// no permission check against the directory's own content and cannot be used
// for reading the directory itself, but IS explicitly documented (open(2)) as
// usable as the dirfd argument to openat/fstatat/unlinkat/renameat/mkdirat/
// symlinkat/linkat/fchmodat and friends — exactly the *at surface this
// package needs, and nothing more.
const rootOpenFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
