//go:build darwin

package procid

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// environ reads the environment block of pid via KERN_PROCARGS2.
//
// The buffer layout is:
//
//	int32           argc
//	char[]          exec_path              (NUL-terminated, then aligned padding)
//	char[]          argv[0..argc-1]        (each NUL-terminated)
//	char[]          envp[0..N-1]           (each NUL-terminated, run ends at buffer end)
//
// Load-bearing caveat, verified on this machine (macOS 26.5.2): the kernel
// truncates the returned buffer for a CROSS-PROCESS same-uid read on
// recent macOS releases — the buffer contains argc + exec_path + argv but
// the environ region is omitted entirely. environ() therefore returns a
// nil slice for such reads on that OS, rather than a partial one, and
// isAgent falls through to the ancestry-walk step. PRP 06's "If this is
// wrong" section explicitly names this scenario and directs us to report,
// not silently ship a weaker design — see docs/knowledge/process-identity.md
// and log.md for the finding.
//
// The self-process case (a process reading its own PID) is still returned
// in full, which is why the parser below is retained rather than gutted.
func environ(pid int) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, fmt.Errorf("procid: sysctl kern.procargs2 %d: %w", pid, err)
	}
	if len(buf) < 4 {
		return nil, fmt.Errorf("procid: procargs2 truncated (%d bytes)", len(buf))
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	p := 4

	// exec_path — read up to and including its NUL, then swallow any
	// additional NUL padding kept for alignment before argv begins.
	end := bytes.IndexByte(buf[p:], 0)
	if end < 0 {
		return nil, fmt.Errorf("procid: procargs2 exec_path unterminated")
	}
	p += end + 1
	for p < len(buf) && buf[p] == 0 {
		p++
	}
	// Skip argc argv strings.
	for i := 0; i < argc && p < len(buf); i++ {
		end := bytes.IndexByte(buf[p:], 0)
		if end < 0 {
			return nil, fmt.Errorf("procid: procargs2 argv[%d] unterminated", i)
		}
		p += end + 1
	}
	// Remainder is the environ, NUL-separated with a trailing empty entry.
	var out []string
	for p < len(buf) {
		end := bytes.IndexByte(buf[p:], 0)
		if end < 0 {
			// Trailing non-terminated bytes — accept as the last entry.
			out = append(out, string(buf[p:]))
			break
		}
		if end == 0 {
			p++
			continue
		}
		out = append(out, string(buf[p:p+end]))
		p += end + 1
	}
	return out, nil
}
