//go:build linux

package procid

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, fmt.Errorf("procid: read /proc/%d/stat: %w", pid, err)
	}
	return parseStatFields(data)
}

// startTime returns the process's start time in clock ticks since boot
// (/proc/<pid>/stat field 22). Any unit stable across one boot is fine —
// the value is only ever compared against another reading of the same PID.
func startTime(pid int) (int64, error) {
	fields, err := readStatFields(pid)
	if err != nil {
		return 0, err
	}
	if len(fields) < 20 {
		return 0, fmt.Errorf("procid: /proc/%d/stat has too few fields", pid)
	}
	return strconv.ParseInt(fields[19], 10, 64)
}

func parent(pid int) (int, error) {
	fields, err := readStatFields(pid)
	if err != nil {
		return 0, err
	}
	if len(fields) < 2 {
		return 0, fmt.Errorf("procid: /proc/%d/stat has too few fields", pid)
	}
	return strconv.Atoi(fields[1])
}
