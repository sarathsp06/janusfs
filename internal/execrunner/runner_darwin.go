//go:build !linux

package execrunner

import "syscall"

func getSysProcAttr(mountpoint string, useChroot bool) *syscall.SysProcAttr {
	return nil
}
