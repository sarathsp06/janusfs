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
	"os"
	"path/filepath"
	"strings"
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
	configFP     atomic.Pointer[map[string]dirConfigFP]
}

// dirConfigFP is one directory's on-disk policy-file fingerprint: whether
// .janusfs.yml exists there and, if so, its mtime and size. Two
// fingerprints differing means the policy on disk has changed since the
// fingerprint was taken — the file appeared, disappeared, or was edited. Size
// is tracked alongside mtime as belt-and-suspenders: an edit that lands in the
// same mtime tick (a coarse-mtime filesystem, or two writes within one
// nanosecond) is still caught as long as it changed the file's length, the
// same reason the redaction cache key carries both.
type dirConfigFP struct {
	ok      bool
	mtimeNS int64
	size    int64
}

func statConfigFP(dir string) dirConfigFP {
	var fp dirConfigFP
	if st, err := os.Stat(filepath.Join(dir, rules.PolicyFileName)); err == nil {
		fp.ok = true
		fp.mtimeNS = st.ModTime().UnixNano()
		fp.size = st.Size()
	}
	return fp
}

// buildConfigSnapshot fingerprints every directory rs already discovered a
// level in. It does NOT walk the tree — it only stats the (small, bounded by
// config-file count, not tree size) set of directories Discover already
// found — so it costs nothing beyond what Discover/Reload already paid.
func buildConfigSnapshot(rs *rules.RuleSet) map[string]dirConfigFP {
	snap := make(map[string]dirConfigFP, len(rs.PolicyDirs)+len(rs.IgnoreLevels)+len(rs.MaskLevels))
	for _, dir := range rs.PolicyDirs {
		if _, ok := snap[dir]; !ok {
			snap[dir] = statConfigFP(dir)
		}
	}
	for _, lvl := range rs.IgnoreLevels {
		if _, ok := snap[lvl.Dir]; !ok {
			snap[lvl.Dir] = statConfigFP(lvl.Dir)
		}
	}
	for _, lvl := range rs.MaskLevels {
		if _, ok := snap[lvl.Dir]; !ok {
			snap[lvl.Dir] = statConfigFP(lvl.Dir)
		}
	}
	return snap
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
	snap := buildConfigSnapshot(rs)
	e.configFP.Store(&snap)
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
	snap := buildConfigSnapshot(rs)
	e.configFP.Store(&snap)
	return nil
}

// StaleAncestors reports whether any .janusfs.yml between
// relPath's own directory (if relPath is itself a directory) or its parent
// (if relPath is a file) and the rule tree's root has appeared, disappeared,
// or changed mtime since the current generation was compiled — including a
// brand-new config file in a directory that previously had none, which a
// naive "did a known file's mtime change" check would miss entirely.
//
// This is bounded by path DEPTH, not tree size: a handful of stat(2) calls
// per ancestor directory, never a tree walk. It is meant to be called from
// Open/Opendir handlers (already off the per-read hot path), never from a
// read handler — every read already re-resolves its decision against
// whichever generation is current (FR-22/FR-24), so a reload triggered here
// takes effect for in-flight reads for free the next time they run.
//
// Global-level (~/.janusfs/config) edits are not covered: that directory
// isn't an ancestor of any in-tree path, so this check can't reach it by
// construction. Those still require an explicit `janusfs update`.
func (e *Engine) StaleAncestors(relPath string, isDir bool) bool {
	rs := e.rs.Load()
	snapPtr := e.configFP.Load()
	if rs == nil || snapPtr == nil {
		return false
	}
	snap := *snapPtr

	dir := filepath.Join(rs.Root, filepath.FromSlash(relPath))
	if !isDir {
		dir = filepath.Dir(dir)
	}
	root := filepath.Clean(rs.Root)

	for {
		dir = filepath.Clean(dir)
		got := statConfigFP(dir)
		want := snap[dir] // zero value if dir wasn't a known level — correct: "no file expected"
		if got != want {
			return true
		}
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// RuleSet exposes the current compiled snapshot for callers that need
// structural access beyond Resolve (e.g. janusfs check's static lint,
// internal/vfsmeta's conflicts.json once it exists) — those are read-only
// consumers of the same immutable snapshot, not a second resolution path.
func (e *Engine) RuleSet() *rules.RuleSet {
	return e.rs.Load()
}
