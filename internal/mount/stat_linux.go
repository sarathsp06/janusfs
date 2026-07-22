//go:build linux

package mount

import "syscall"

// getMtimeNS extracts the modification time in nanoseconds from a Linux stat structure.
func getMtimeNS(st *syscall.Stat_t) int64 {
	return st.Mtim.Sec*1e9 + st.Mtim.Nsec
}
