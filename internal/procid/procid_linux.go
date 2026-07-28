//go:build linux

package procid

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// parseStatFields splits the /proc/<pid>/stat contents starting AFTER the
// executable name. Field 2 (comm) is wrapped in parentheses and may itself
// contain spaces or parentheses, so the safe split is on the LAST ')' in
// the line — never naive whitespace-splitting from the start, which would
// misalign every subsequent field for any process whose executable name
// contains a space or paren. Extracted from the file read so it can be
// unit-tested against fixed inputs.
func parseStatFields(data []byte) ([]string, error) {
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+2 >= len(data) {
		return nil, fmt.Errorf("procid: malformed /proc stat line")
	}
	// After ") ", fields[0] is state (originally field 3), so ppid is
	// fields[1] and starttime is fields[19].
	return strings.Fields(string(data[i+2:])), nil
}

func readStatFields(pid int) ([]string, error) {
	var buf [32]byte
	b := append(buf[:0], "/proc/"...)
	b = strconv.AppendInt(b, int64(pid), 10)
	b = append(b, "/stat"...)
	path := string(b)

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("procid: open %s: %w", path, err)
	}
	defer unix.Close(fd)

	var data [1024]byte
	n, err := unix.Read(fd, data[:])
	if err != nil {
		return nil, fmt.Errorf("procid: read %s: %w", path, err)
	}

	return parseStatFields(data[:n])
}

func parentAndStartTime(pid int) (int, int64, error) {
	var buf [32]byte
	b := append(buf[:0], "/proc/"...)
	b = strconv.AppendInt(b, int64(pid), 10)
	b = append(b, "/stat"...)
	path := string(b)

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("procid: open %s: %w", path, err)
	}
	defer unix.Close(fd)

	var data [1024]byte
	n, err := unix.Read(fd, data[:])
	if err != nil {
		return 0, 0, fmt.Errorf("procid: read %s: %w", path, err)
	}
	subData := data[:n]

	i := bytes.LastIndexByte(subData, ')')
	if i < 0 || i+2 >= len(subData) {
		return 0, 0, fmt.Errorf("procid: malformed /proc stat line")
	}
	sub := subData[i+2:]

	var ppid int
	var starttime int64
	var foundPPID, foundStarttime bool

	fieldIdx := 0
	inField := false
	var fieldStart int

	for idx := 0; idx <= len(sub); idx++ {
		var isSpace bool
		if idx < len(sub) {
			c := sub[idx]
			isSpace = c == ' ' || c == '\t' || c == '\r' || c == '\n'
		} else {
			isSpace = true
		}

		if inField {
			if isSpace {
				if fieldIdx == 1 {
					val, err := parseInt(sub[fieldStart:idx])
					if err != nil {
						return 0, 0, fmt.Errorf("procid: parsing ppid: %w", err)
					}
					ppid = int(val)
					foundPPID = true
				} else if fieldIdx == 19 {
					val, err := parseInt(sub[fieldStart:idx])
					if err != nil {
						return 0, 0, fmt.Errorf("procid: parsing starttime: %w", err)
					}
					starttime = val
					foundStarttime = true
					break
				}
				inField = false
				fieldIdx++
			}
		} else {
			if !isSpace {
				fieldStart = idx
				inField = true
			}
		}
	}

	if !foundPPID {
		return 0, 0, fmt.Errorf("procid: ppid field not found")
	}
	if !foundStarttime {
		return 0, 0, fmt.Errorf("procid: starttime field not found")
	}

	return ppid, starttime, nil
}

func parseInt(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("empty")
	}
	var neg bool
	if b[0] == '-' {
		neg = true
		b = b[1:]
	} else if b[0] == '+' {
		b = b[1:]
	}
	var n int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid digit")
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// startTime returns the process's start time in clock ticks since boot
// (/proc/<pid>/stat field 22). Any unit stable across one boot is fine —
// the value is only ever compared against another reading of the same PID.
func startTime(pid int) (int64, error) {
	_, st, err := parentAndStartTime(pid)
	return st, err
}

func parent(pid int) (int, error) {
	p, _, err := parentAndStartTime(pid)
	return p, err
}
