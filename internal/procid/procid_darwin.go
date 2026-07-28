//go:build darwin

package procid

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func parentAndStartTime(pid int) (int, int64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, fmt.Errorf("procid: sysctl kern.proc.pid %d: %w", pid, err)
	}
	tv := kp.Proc.P_starttime
	st := int64(tv.Sec)*1_000_000 + int64(tv.Usec)
	return int(kp.Eproc.Ppid), st, nil
}

// startTime returns the process's boot-relative start time in microseconds.
// Any unit that is stable across a single boot is fine — the value is only
// ever compared against another reading of the same PID to detect reuse.
func startTime(pid int) (int64, error) {
	_, st, err := parentAndStartTime(pid)
	return st, err
}

func parent(pid int) (int, error) {
	p, _, err := parentAndStartTime(pid)
	return p, err
}
