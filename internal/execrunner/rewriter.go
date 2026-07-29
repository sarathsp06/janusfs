//go:build darwin

// Path-string rewriting exists only to simulate path parity on a platform
// with no per-process mount namespaces (see runner.go's package doc) — hence
// darwin-only.
package execrunner

import "bytes"

func isNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_'
}

func isPathChar(c byte) bool {
	return isNameChar(c) || c == '/'
}

// ReplacePaths replaces occurrences of oldPath with newPath in buf,
// enforcing path-boundary constraints.
func ReplacePaths(buf []byte, oldPath []byte, newPath []byte) []byte {
	if len(oldPath) == 0 {
		return buf
	}
	var result []byte
	i := 0
	for i < len(buf) {
		idx := bytes.Index(buf[i:], oldPath)
		if idx == -1 {
			result = append(result, buf[i:]...)
			break
		}
		realIdx := i + idx
		// Check boundaries
		prevValid := realIdx == 0 || !isPathChar(buf[realIdx-1])
		nextIdx := realIdx + len(oldPath)
		nextValid := nextIdx == len(buf) || buf[nextIdx] == '/' || !isNameChar(buf[nextIdx])

		if prevValid && nextValid {
			result = append(result, buf[i:realIdx]...)
			result = append(result, newPath...)
			i = nextIdx
		} else {
			result = append(result, buf[i:realIdx+1]...)
			i = realIdx + 1
		}
	}
	return result
}
