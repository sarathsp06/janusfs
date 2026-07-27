// Package obs holds the observability internals: event bus,
// metrics registry, ring buffer, and top-N path tracker. It is the single
// point where FUSE handlers emit structured events about every decision and
// read — handlers use non-blocking send so observability never blocks the
// data path.
package obs

import (
	"fmt"
	"time"
)

// Decision is the resolved state for a path, mirrored from engine.Decision
// so this package has no engine dependency.
type Decision uint8

const (
	Allowed Decision = iota
	Masked
	Hidden
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "ALLOWED"
	case Masked:
		return "MASKED"
	case Hidden:
		return "HIDDEN"
	default:
		return "UNKNOWN"
	}
}

// Op is the FUSE operation type.
type Op string

const (
	OpLookup   Op = "lookup"
	OpGetattr  Op = "getattr"
	OpOpen     Op = "open"
	OpRead     Op = "read"
	OpReaddir  Op = "readdir"
	OpWrite    Op = "write"
	OpCreate   Op = "create"
	OpUnlink   Op = "unlink"
	OpMkdir    Op = "mkdir"
	OpRmdir    Op = "rmdir"
	OpRename   Op = "rename"
	OpSymlink  Op = "symlink"
	OpReadlink Op = "readlink"
	OpSetattr  Op = "setattr"
	OpGetxattr Op = "getxattr"
)

// CacheResult describes how a read was served.
type CacheResult string

const (
	CacheHit     CacheResult = "hit"
	CacheMiss    CacheResult = "miss"
	CacheRebuild CacheResult = "rebuild"
	CacheNA      CacheResult = "na"
)

// Event is emitted on every decision-bearing FUSE op.
type Event struct {
	TS          time.Time
	Op          Op
	Path        string
	Decision    Decision
	MatchedRule string
	Patterns    []string
	Bytes       int64
	LatencyUs   int64
	Cache       CacheResult
	Err         error
}

// Label returns a short one-line summary for the ring buffer display.
func (e Event) Label() string {
	s := string(e.Op) + " " + e.Decision.String() + " " + e.Path
	if len(e.Patterns) > 0 {
		s += " [" + e.Patterns[0] + "]"
	}
	if e.Cache != "" && e.Cache != CacheNA {
		s += " (" + string(e.Cache) + ")"
	}
	if e.Bytes > 0 {
		s += " " + formatBytes(e.Bytes)
	}
	return s
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
