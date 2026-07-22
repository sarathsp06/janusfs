//go:build darwin

package mount

import "syscall"

// getMtimeNS extracts the modification time in nanoseconds from a macOS stat structure.
func getMtimeNS(st *syscall.Stat_t) int64 {
	return st.Mtimespec.Sec*1e9 + st.Mtimespec.Nsec
}
