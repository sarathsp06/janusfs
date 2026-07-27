package nsexec

import (
	"bytes"
	"strconv"
	"strings"
)

// nullTerminatedString trims a fixed-size, NUL-padded byte array (as returned
// by uname(2) via unix.Utsname) to its string content. Portable, pure-Go logic
// with no platform dependency, kept separate from support_linux.go so it can
// be unit-tested on any development machine, not only on Linux.
func nullTerminatedString(b []byte) string {
	if n := bytes.IndexByte(b, 0); n >= 0 {
		b = b[:n]
	}
	return string(b)
}

// parseKernelVersion extracts the leading "major.minor" from a uname release
// string such as "5.15.0-91-generic" or "6.8.0-1015-aws".
func parseKernelVersion(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minorDigits := leadingDigits(parts[1])
	if minorDigits == "" {
		return 0, 0, false
	}
	minor, errMinor := strconv.Atoi(minorDigits)
	if errMajor != nil || errMinor != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
