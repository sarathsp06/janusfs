// Package engine implements the decision engine (SPEC.md §7): pure,
// synchronous resolution of a path to Allowed/Masked/Hidden, backed by an
// immutable rule-set snapshot swapped atomically on reload.
//
// Decision resolution itself (precedence, hierarchical discovery,
// fail-closed folding) lives in internal/rules, which is directly testable
// against a temp directory tree with no engine involved (NFR-8). This
// package's job is the SPEC §7 contract: hold the current compiled
// snapshot behind an atomic.Pointer so FUSE handlers (once wired, Phase 1
// mount integration) never block on a reload in progress (NFR-5), and
// track a Generation counter so callers can invalidate anything keyed to a
// stale rule set (FR-19, once the watcher lands in Phase 3).
package engine

import (
	"sync/atomic"

	"github.com/sarathsp06/janusfs/internal/patterns"
	"github.com/sarathsp06/janusfs/internal/rules"
)

// Decision re-exports rules.Decision: SPEC §7 treats the decision engine as
// the type's conceptual home, but the enum and its matching logic can only
// be correctly defined once, next to Resolve — see internal/rules's package
// doc. Constants are re-exported alongside it so callers never need to
// import internal/rules just to name Allowed/Masked/Hidden.
type Decision = rules.Decision

const (
	Allowed = rules.Allowed
	Masked  = rules.Masked
	Hidden  = rules.Hidden
)

// Resolution is SPEC §7's Resolution, extended with the same explainability
// fields internal/rules.Resolution carries (RuleRef, Trace) — see that
// package's doc for why.
type Resolution struct {
	Decision     Decision
	RuleRef      string
	PatternNames []string
	// Patterns is rules.Resolution.Patterns passed through unchanged — the
	// compiled patterns.Pattern objects internal/provider needs to redact
	// (see that field's doc for why this isn't derived from PatternNames).
	Patterns   []*patterns.Pattern
	Poisoned   bool
	Trace      []rules.TraceEntry
	Generation uint64
}

// Engine implements SPEC §7's Engine interface: Resolve never errors
// (rules.RuleSet.Resolve already folds every internal error to Hidden, per
// FR-6), and the current rule set is held behind an atomic.Pointer so a
// concurrent Reload (Phase 3) never blocks a resolver (NFR-5).
type Engine struct {
	rs  atomic.Pointer[rules.RuleSet]
	gen atomic.Uint64
}

// New discovers root's rule tree (SPEC §15 step 5: "Construct internal/rules
// (initial compile) -> internal/engine") and returns an Engine ready to
// resolve paths against it, at generation 1.
func New(root string) (*Engine, error) {
	rs, err := rules.Discover(root)
	if err != nil {
		return nil, err
	}
	e := &Engine{}
	e.rs.Store(rs)
	e.gen.Store(1)
	return e, nil
}

// Resolve implements FR-5..FR-9 for relPath (relative to the engine's
// root), against whichever rule-set generation is current at call time.
func (e *Engine) Resolve(relPath string, isDir bool) Resolution {
	rs := e.rs.Load()
	res := rs.Resolve(relPath, isDir)
	return Resolution{
		Decision:     res.Decision,
		RuleRef:      res.RuleRef,
		PatternNames: res.PatternNames,
		Patterns:     res.Patterns,
		Poisoned:     res.Poisoned,
		Trace:        res.Trace,
		Generation:   e.gen.Load(),
	}
}

// Generation returns the current compiled rule-set generation (FR-19).
func (e *Engine) Generation() uint64 {
	return e.gen.Load()
}

// Reload recompiles root's rule tree and atomically swaps it in as the new
// current generation. Readers already in Resolve either see the old or the
// new snapshot, never a half-built one (SPEC §7: "recompiler builds a full
// new snapshot off-thread and swaps it; readers never lock").
func (e *Engine) Reload(root string) error {
	rs, err := rules.Discover(root)
	if err != nil {
		return err
	}
	e.rs.Store(rs)
	e.gen.Add(1)
	return nil
}

// RuleSet exposes the current compiled snapshot for callers that need
// structural access beyond Resolve (e.g. janusfs check's static lint,
// internal/vfsmeta's conflicts.json once it exists) — those are read-only
// consumers of the same immutable snapshot, not a second resolution path.
func (e *Engine) RuleSet() *rules.RuleSet {
	return e.rs.Load()
}
