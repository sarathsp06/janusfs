//go:build !darwin

package health

import "syscall"

func checkMacFUSE() MacFUSEStatus {
	return MacFUSEStatus{Installed: false, Loaded: false}
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
