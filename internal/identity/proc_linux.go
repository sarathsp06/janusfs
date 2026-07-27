//go:build linux

package identity

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func getOSProcessInfo(pid int) (ppid int, startSec int64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	lastRP := bytes.LastIndexByte(data, ')')
	if lastRP == -1 || lastRP+2 >= len(data) {
		return 0, 0, fmt.Errorf("invalid stat format")
	}
	rest := string(data[lastRP+2:])
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return 0, 0, fmt.Errorf("too few fields in stat")
	}
	// fields[0] is state (field 3)
	// fields[1] is ppid (field 4)
	ppidVal, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid ppid %q: %w", fields[1], err)
	}
	// fields[19] is starttime (field 22)
	startTicks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid starttime %q: %w", fields[19], err)
	}
	return ppidVal, startTicks, nil
}

func getBootUUID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
