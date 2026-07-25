//go:build linux

package execrunner

import (
	"os"
	"syscall"
)

func getSysProcAttr(mountpoint string, useChroot bool) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getuid(),
				Size:        1,
			},
		},
		GidMappings: []syscall.SysProcIDMap{
			{
				ContainerID: 0,
				HostID:      os.Getgid(),
				Size:        1,
			},
		},
		GidMappingsEnableSetgroups: false,
	}
	if useChroot && mountpoint != "" {
		attr.Chroot = mountpoint
	}
	return attr
}
