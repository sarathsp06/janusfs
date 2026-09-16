//go:build !linux

package health

import (
	"os"
	"syscall"
)

func checkFUSE() FUSEStatus {
	s := FUSEStatus{}
	// On Linux and other Unix systems, FUSE is active if /dev/fuse is accessible.
	if _, err := os.Stat("/dev/fuse"); err == nil {
		s.Installed = true
		s.Loaded = true
	}
	return s
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func platformWarnings() []string { return nil }
