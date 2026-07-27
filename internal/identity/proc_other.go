//go:build !darwin && !linux

package identity

func getOSProcessInfo(pid int) (ppid int, startSec int64, err error) {
	return 1, 12345678, nil
}

func getBootUUID() (string, error) {
	return "00000000-0000-0000-0000-000000000000", nil
}
