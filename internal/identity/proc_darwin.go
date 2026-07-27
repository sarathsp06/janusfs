//go:build darwin

package identity

import (
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/unix"
)

func getOSProcessInfo(pid int) (ppid int, startSec int64, err error) {
	mib := []int32{unix.CTL_KERN, unix.KERN_PROC, unix.KERN_PROC_PID, int32(pid)}
	size := uintptr(0)
	err = unix.Sysctl(mib, nil, &size, nil, 0)
	if err != nil {
		return 0, 0, err
	}
	if size == 0 {
		return 0, 0, fmt.Errorf("process not found")
	}
	buf := make([]byte, size)
	err = unix.Sysctl(mib, &buf[0], &size, nil, 0)
	if err != nil {
		return 0, 0, err
	}
	if len(buf) < 48 {
		return 0, 0, fmt.Errorf("buffer too small: %d", len(buf))
	}
	// Parse starting from the computed offsets:
	// tv_sec is at offset 0 (8 bytes)
	startSec = int64(binary.LittleEndian.Uint64(buf[0:8]))
	// ppid is at offset 44 (4 bytes)
	ppid = int(binary.LittleEndian.Uint32(buf[44:48]))
	return ppid, startSec, nil
}

func getBootUUID() (string, error) {
	return unix.Sysctl("kern.bootsessionuuid")
}
