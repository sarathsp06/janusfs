package execrunner

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// SessionStats counts what the boundary actually did during one exec
// session, so the operator gets one line of feedback at the end — "3 masked
// files were read, 2 hidden paths were denied" — instead of silence. It is
// fed from the FUSE adapter's Observe callback, which runs synchronously on
// the data path, so Record is a mutex and a couple of map writes and nothing
// more.
//
// Only paths and counts are kept, never bytes read through the mount: the
// summary is printed to the operator's terminal and must not become the
// leak channel the rest of the tool exists to close.
type SessionStats struct {
	mu     sync.Mutex
	ops    uint64
	masked map[string]int64 // path -> masked bytes served
	hidden map[string]uint64
	faults uint64
}

// Record ingests one FUSE operation. decision is the adapter's label:
// ALLOWED, MASKED, HIDDEN, PANIC, or CONFIG_READONLY.
func (s *SessionStats) Record(op, path, decision string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	switch decision {
	case "MASKED":
		if op == "read" && bytes > 0 {
			if s.masked == nil {
				s.masked = map[string]int64{}
			}
			s.masked[path] += bytes
		}
	case "HIDDEN":
		if s.hidden == nil {
			s.hidden = map[string]uint64{}
		}
		s.hidden[path]++
	case "PANIC":
		s.faults++
	}
}

// Summary renders the one-line end-of-session report.
func (s *SessionStats) Summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var parts []string
	if n := len(s.masked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d masked file(s) read (%s)", n, examples(keysOf(s.masked))))
	}
	if n := len(s.hidden); n > 0 {
		parts = append(parts, fmt.Sprintf("%d hidden path(s) denied (%s)", n, examples(keysOf(s.hidden))))
	}
	if s.faults > 0 {
		parts = append(parts, fmt.Sprintf("%d internal fault(s) failed closed", s.faults))
	}
	if len(parts) == 0 {
		parts = append(parts, "no masked or hidden paths were touched")
	}
	return fmt.Sprintf("%s · %d fs op(s)", strings.Join(parts, ", "), s.ops)
}

// examples shows up to three paths, sorted for a stable line, and counts the
// rest rather than flooding the terminal after a long session.
func examples(paths []string) string {
	sort.Strings(paths)
	const max = 3
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(paths[:max], ", "), len(paths)-max)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
