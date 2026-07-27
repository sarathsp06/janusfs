//go:build darwin

package backing

import "golang.org/x/sys/unix"

// rootOpenFlags on darwin: macOS has no O_PATH, so O_RDONLY|O_DIRECTORY
// serves as the openat(2) base instead — it just also permits reading the
// directory's own content, which this package never does.
const rootOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
