//go:build linux

package procid

import (
	"bytes"
	"fmt"
	"os"
)

// environ reads the environment block of pid via /proc/<pid>/environ, whose
// contents are the process's initial environment separated by NUL bytes.
// Kernel visibility is the same as ptrace_may_access: a same-uid process is
// always allowed.
func environ(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, fmt.Errorf("procid: read /proc/%d/environ: %w", pid, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	// Trim the trailing NUL that /proc/<pid>/environ ends with, so Split
	// does not produce an empty tail entry.
	if data[len(data)-1] == 0 {
		data = data[:len(data)-1]
	}
	parts := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out, nil
}
