//go:build darwin

package procid

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// startTime returns the process's boot-relative start time in microseconds.
// Any unit that is stable across a single boot is fine — the value is only
// ever compared against another reading of the same PID to detect reuse.
func startTime(pid int) (int64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("procid: sysctl kern.proc.pid %d: %w", pid, err)
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec), nil
}

func parent(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("procid: sysctl kern.proc.pid %d: %w", pid, err)
	}
	return int(kp.Eproc.Ppid), nil
}
