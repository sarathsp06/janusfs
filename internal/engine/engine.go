// Package engine implements the decision engine: pure,
// synchronous resolution of a path to Allowed/Masked/Hidden, backed by an
// immutable rule-set snapshot swapped atomically on reload.
//
// Decision resolution itself (precedence, hierarchical discovery,
// fail-closed folding) lives in internal/rules, which is directly testable
// against a temp directory tree with no engine involved. This package's job is
// narrower: hold the current compiled snapshot behind an atomic.Pointer so FUSE
// handlers never block on a reload in progress, and track a Generation counter
// so callers can invalidate anything keyed to a stale rule set.
package engine

import (
	"sync"
	"sync/atomic"

	"github.com/sarathsp06/janusfs/internal/patterns"
	"github.com/sarathsp06/janusfs/internal/rules"
)

// decisionCacheMax bounds the memoized-decision cache. Exceeding it drops the
// whole cache rather than maintaining an LRU.
//
// ponytail: whole-cache drop past a fixed entry ceiling, not an LRU. Decisions
// are cheap to recompute (a cold cache is a performance question, not a
// correctness one) and the generation key already makes reload-invalidation
// free; add an LRU only if profiling ever shows the drop causing real churn.
// The bound exists because an untrusted caller can `stat` unlimited distinct
// nonexistent paths, each producing a cache entry — an unbounded map would be
// a memory-growth vector reachable from the agent side of the mount.
const decisionCacheMax = 100_000

// Decision re-exports rules.Decision. This package is the decision engine and
// so the type's conceptual home, but the enum and its matching logic can only be
// correctly defined once, next to Resolve — see internal/rules's package
// doc. Constants are re-exported alongside it so callers never need to
// import internal/rules just to name Allowed/Masked/Hidden.
type Decision = rules.Decision

const (
	Allowed = rules.Allowed
	Masked  = rules.Masked
	Hidden  = rules.Hidden
)

// Resolution mirrors rules.Resolution, carrying the same explainability fields
// (RuleRef, Trace) — see that package's doc for why they exist.
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

// decisionKey identifies one memoized Resolve call. gen is part of the key —
// not a separately-checked field — so a generation bump makes every existing
// entry unreachable for free: no sweep, no eviction pass, no coordination with
// Reload beyond incrementing a counter.
type decisionKey struct {
	relPath string
	isDir   bool
	gen     uint64
}

// Engine is the resolution entry point. Resolve never errors, because
// rules.RuleSet.Resolve already folds every internal error to Hidden, and the
// current rule set is held behind an atomic.Pointer so a concurrent Reload never
// blocks a resolver.
//
// Resolve is memoized in cache, keyed by decisionKey. cache is held behind an
// atomic.Pointer[sync.Map] (rather than a plain sync.Map field) so
// decisionCacheMax's overflow handling can swap in a fresh, empty map instead
// of maintaining an LRU.
type Engine struct {
	rs           atomic.Pointer[rules.RuleSet]
	gen          atomic.Uint64
	cache        atomic.Pointer[sync.Map]
	cacheEntries atomic.Int64
}

// New discovers and compiles root's rule tree, returning an Engine ready to
// resolve paths against it, at generation 1.
func New(root string) (*Engine, error) {
	rs, err := rules.Discover(root)
	if err != nil {
		return nil, err
	}
	e := &Engine{}
	e.rs.Store(rs)
	e.gen.Store(1)
	e.cache.Store(&sync.Map{})
	return e, nil
}

// Resolve returns the Decision for relPath (relative to the engine's root),
// against whichever rule-set generation is current at call time. Memoized: a
// repeat call with the same (relPath, isDir) against the same generation
// returns the cached Resolution rather than re-walking the rule hierarchy.
//
// Cached Resolution values are shared across concurrent callers, so no caller
// may mutate one in place (append to PatternNames/Patterns/Trace, etc.) —
// every current caller only reads them.
func (e *Engine) Resolve(relPath string, isDir bool) Resolution {
	// Generation is loaded BEFORE the rule set, deliberately. Loading it after
	// would let a concurrent Reload race in between: rs swaps to the new
	// snapshot, gen bumps, and this call would then compute against the OLD
	// snapshot (captured before the swap in a subsequent line) but cache the
	// result under the NEW generation's key — a real caller resolving that
	// same path under the new generation would then hit the cache and
	// silently get stale, pre-reload policy. Loading gen first means the worst
	// case is the reverse: a decision computed from the NEW snapshot gets
	// cached under the OLD (soon-to-be-unreachable) generation key, which is
	// merely a wasted entry, never a stale serve.
	gen := e.gen.Load()
	cache := e.cache.Load()

	k := decisionKey{relPath: relPath, isDir: isDir, gen: gen}
	if v, ok := cache.Load(k); ok {
		return v.(Resolution)
	}

	rs := e.rs.Load()
	res := rs.Resolve(relPath, isDir)
	out := Resolution{
		Decision:     res.Decision,
		RuleRef:      res.RuleRef,
		PatternNames: res.PatternNames,
		Patterns:     res.Patterns,
		Poisoned:     res.Poisoned,
		Trace:        res.Trace,
		Generation:   gen,
	}

	if e.cacheEntries.Add(1) > decisionCacheMax {
		// Bound exceeded: drop the whole cache. CompareAndSwap against the
		// exact map this call loaded means only one racing goroutine actually
		// performs the swap; the rest no-op harmlessly.
		if e.cache.CompareAndSwap(cache, &sync.Map{}) {
			e.cacheEntries.Store(0)
		}
	} else {
		cache.Store(k, out)
	}
	return out
}

// Generation returns the current compiled rule-set generation.
func (e *Engine) Generation() uint64 {
	return e.gen.Load()
}

// Reload recompiles root's rule tree and atomically swaps it in as the new
// current generation. Readers already in Resolve either see the old or the
// new snapshot, never a half-built one: the recompile happens off-thread and the
// result is swapped in atomically, so readers never lock.
//
// The decision cache is dropped too. The generation bump already makes every
// existing entry unreachable by key, but dropping the map itself is what
// actually frees that memory rather than leaving it retained-but-orphaned
// across many reloads.
func (e *Engine) Reload(root string) error {
	rs, err := rules.Discover(root)
	if err != nil {
		return err
	}
	e.rs.Store(rs)
	e.gen.Add(1)
	e.cache.Store(&sync.Map{})
	e.cacheEntries.Store(0)
	return nil
}

// RuleSet exposes the current compiled snapshot for callers that need
// structural access beyond Resolve (e.g. janusfs check's static lint,
// internal/vfsmeta's conflicts.json once it exists) — those are read-only
// consumers of the same immutable snapshot, not a second resolution path.
func (e *Engine) RuleSet() *rules.RuleSet {
	return e.rs.Load()
}
