//go:build !darwin

package health

import (
	"os"
	"syscall"
)

func checkMacFUSE() MacFUSEStatus {
	s := MacFUSEStatus{}
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
