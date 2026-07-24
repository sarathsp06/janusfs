package execrunner

import (
	"bytes"
	"io"
)

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

// longestPrefixSuffix returns the length of the longest suffix of buf
// (of length < len(M)) that is a prefix of M.
func longestPrefixSuffix(buf []byte, M []byte) int {
	maxL := len(buf)
	if maxL >= len(M) {
		maxL = len(M) - 1
	}
	for l := maxL; l > 0; l-- {
		suffix := buf[len(buf)-l:]
		if bytes.Equal(suffix, M[:l]) {
			return l
		}
	}
	return 0
}

// StreamRewriter intercepts an io.Writer and replaces oldPath with newPath.
type StreamRewriter struct {
	w       io.Writer
	oldPath []byte
	newPath []byte
	buf     []byte
}

func NewStreamRewriter(w io.Writer, oldPath, newPath string) *StreamRewriter {
	return &StreamRewriter{
		w:       w,
		oldPath: []byte(oldPath),
		newPath: []byte(newPath),
	}
}

func (sr *StreamRewriter) Write(p []byte) (int, error) {
	sr.buf = append(sr.buf, p...)
	if len(sr.oldPath) == 0 {
		n, err := sr.w.Write(sr.buf)
		sr.buf = sr.buf[n:]
		return len(p), err
	}

	// Find the longest suffix of sr.buf that could be a prefix of oldPath.
	l := longestPrefixSuffix(sr.buf, sr.oldPath)
	safeLen := len(sr.buf) - l

	if safeLen > 0 {
		safeBuf := sr.buf[:safeLen]
		replaced := ReplacePaths(safeBuf, sr.oldPath, sr.newPath)
		if _, err := sr.w.Write(replaced); err != nil {
			return 0, err
		}
		// Keep only the unsafe suffix in sr.buf.
		sr.buf = sr.buf[safeLen:]
	}
	return len(p), nil
}

func (sr *StreamRewriter) Flush() error {
	if len(sr.buf) > 0 {
		replaced := ReplacePaths(sr.buf, sr.oldPath, sr.newPath)
		if _, err := sr.w.Write(replaced); err != nil {
			return err
		}
		sr.buf = nil
	}
	return nil
}

func (sr *StreamRewriter) Close() error {
	return sr.Flush()
}
