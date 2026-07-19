package rules

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

// TraceEntry records one rule's contribution to a Resolve call, for
// janusfs explain (docs/SPEC_AMENDMENTS.md 2026-07-17) and janusfs check.
type TraceEntry struct {
	File    string
	LineNo  int
	Kind    string // "ignore" | "mask" | "config_error"
	Line    string
	Matched bool
	Negated bool
}

// Resolution is the resolved state of one path plus the evidence behind it
// (SPEC §7's Resolution, extended with a Trace for explainability — the
// normative fields Decision/RuleRef/Patterns/Generation are unaffected;
// Trace is additive).
type Resolution struct {
	Decision     Decision
	RuleRef      string // "<file>:<line>" of the deciding rule, "" if none matched
	PatternNames []string
	// Patterns holds the same deduplicated set as PatternNames, but as the
	// actual compiled *patterns.Pattern objects (§8.1's CompiledPattern) —
	// internal/provider needs these to redact, not just their names.
	// Parallel to PatternNames: Patterns[i].Name == PatternNames[i].
	Patterns []*patterns.Pattern
	Poisoned bool // true if a config error forced this to Hidden (FR-13)
	Trace    []TraceEntry
}

// Resolve implements FR-5..FR-9's precedence (Hidden > Masked > Allowed) for
// relPath (relative to rs.Root), including the FR-8 ancestor short-circuit:
// a Hidden ancestor directory forces every descendant Hidden regardless of
// deeper rules. It never errors: any internal inconsistency (e.g. a path
// outside Root) folds to Hidden, matching FR-6's fail-closed tiebreak
// (SPEC §20.2).
//
// Directories are never Masked (FR-9): for isDir==true this only ever
// returns Allowed or Hidden.
func (rs *RuleSet) Resolve(relPath string, isDir bool) Resolution {
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	if relPath == "." {
		relPath = ""
	}

	for _, ancestor := range ancestorDirs(relPath) {
		if hidden, ref, poisoned, trace := rs.resolveIgnore(ancestor, true); hidden {
			return Resolution{Decision: Hidden, RuleRef: ref, Poisoned: poisoned, Trace: trace}
		}
	}

	hidden, ref, poisoned, trace := rs.resolveIgnore(relPath, isDir)
	if hidden {
		return Resolution{Decision: Hidden, RuleRef: ref, Poisoned: poisoned, Trace: trace}
	}

	if isDir {
		return Resolution{Decision: Allowed, Trace: trace}
	}

	poisonedMask := false
	poisonedRef := ""
	seenNames := map[string]bool{}
	patByName := map[string]*patterns.Pattern{}
	var patternNames []string
	maskRef := ""

	for _, lvl := range rs.applicableMaskLevels(relPath) {
		relToLevel := relativeToLevel(rs.Root, lvl.Dir, relPath)

		for _, entry := range lvl.Entries {
			if entry.GlobPattern == nil || !entry.GlobPattern.matches(relToLevel, false) {
				continue
			}
			ref := lvl.File + ":" + strconv.Itoa(entry.LineNo)
			if entry.CompileErr != nil {
				poisonedMask = true
				poisonedRef = ref
				trace = append(trace, TraceEntry{
					File: lvl.File, LineNo: entry.LineNo, Kind: "config_error",
					Line: entry.Glob, Matched: true,
				})
				continue
			}
			if maskRef == "" {
				maskRef = ref
			}
			trace = append(trace, TraceEntry{
				File: lvl.File, LineNo: entry.LineNo, Kind: "mask",
				Line: entry.Glob, Matched: true,
			})
			for _, p := range entry.Patterns {
				if !seenNames[p.Name] {
					seenNames[p.Name] = true
					patternNames = append(patternNames, p.Name)
					patByName[p.Name] = p
				}
			}
		}
	}

	if poisonedMask {
		return Resolution{Decision: Hidden, Poisoned: true, RuleRef: poisonedRef, Trace: trace}
	}

	if len(patternNames) > 0 {
		sort.Strings(patternNames)
		pats := make([]*patterns.Pattern, len(patternNames))
		for i, n := range patternNames {
			pats[i] = patByName[n]
		}
		return Resolution{Decision: Masked, RuleRef: maskRef, PatternNames: patternNames, Patterns: pats, Trace: trace}
	}

	return Resolution{Decision: Allowed, Trace: trace}
}

// resolveIgnore evaluates the ignore levels applicable to relPath (global +
// ancestor-or-self directories), in shallowest-first order, applying
// gitignore's "later match wins" (FR-12) — with one precedence change on
// top, per docs/SPEC_AMENDMENTS.md (2026-07-18, "Rule precedence"): the
// global level is a fail-closed floor. Once the global level's own
// (self-consistent) evaluation decides a path is Hidden, no in-tree level
// (mount root or any subdirectory) may negate that verdict — negation
// still works exactly as before *within* the in-tree tier (a deeper
// in-tree file can still override a shallower in-tree file), and *within*
// the global level itself (a later global line can still override an
// earlier one there). Only an in-tree actor lifting a global verdict is
// blocked — this is the cross-trust-boundary property FR-15's read-only
// config was already protecting; see the amendment for the full rationale.
//
// A poisoned level (a line that failed to compile) forces Hidden for every
// path it would otherwise cover, per IgnoreLevel.Poisoned's doc; the
// poisoned return lets Resolve set Resolution.Poisoned (FR-13) regardless
// of which tier the poisoned level belongs to.
func (rs *RuleSet) resolveIgnore(relPath string, isDir bool) (hidden bool, ruleRef string, poisoned bool, trace []TraceEntry) {
	floorHidden := false

	for _, lvl := range rs.applicableIgnoreLevels(relPath) {
		isGlobalTier := rs.GlobalDir != "" && lvl.Dir == rs.GlobalDir

		if lvl.Poisoned {
			hidden = true
			poisoned = true
			ruleRef = lvl.File
			trace = append(trace, TraceEntry{File: lvl.File, Kind: "config_error", Matched: true})
			if isGlobalTier {
				floorHidden = true
			}
			continue
		}

		relToLevel := relativeToLevel(rs.Root, lvl.Dir, relPath)
		for _, p := range lvl.Patterns {
			if !p.matches(relToLevel, isDir) {
				continue
			}
			wantHidden := !p.negate
			if !isGlobalTier && !wantHidden && floorHidden {
				// The global tier already decided this path is Hidden; an
				// in-tree negation cannot lift that floor. Record the
				// attempt (janusfs check/explain surface it) but don't
				// apply it.
				trace = append(trace, TraceEntry{
					File: lvl.File, LineNo: p.lineNo, Kind: "ignore",
					Line: p.raw, Matched: false, Negated: true,
				})
				continue
			}
			hidden = wantHidden
			ruleRef = lvl.File + ":" + strconv.Itoa(p.lineNo)
			trace = append(trace, TraceEntry{
				File: lvl.File, LineNo: p.lineNo, Kind: "ignore",
				Line: p.raw, Matched: hidden, Negated: p.negate,
			})
		}

		if isGlobalTier {
			floorHidden = hidden
		}
	}
	return hidden, ruleRef, poisoned, trace
}

// ancestorDirs returns the relative-path ancestors of relPath, shallowest
// first, excluding relPath itself and the root ("" is never included).
// For "a/b/c.txt" this is ["a", "a/b"]; for "a" (whether file or dir) it is
// empty (no ancestor besides the root, which carries no path of its own).
func ancestorDirs(relPath string) []string {
	if relPath == "" {
		return nil
	}
	segs := strings.Split(relPath, "/")
	var out []string
	for i := 1; i < len(segs); i++ {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return out
}

// relativeToLevel computes relPath (relative to root) expressed relative
// to a level directory lvlDir (absolute), in slash form.
func relativeToLevel(root, lvlDir, relPath string) string {
	full := filepath.Join(root, relPath)
	rel, err := filepath.Rel(lvlDir, full)
	if err != nil {
		return relPath
	}
	return filepath.ToSlash(rel)
}

func (rs *RuleSet) applicableIgnoreLevels(relPath string) []IgnoreLevel {
	full := filepath.Join(rs.Root, relPath)
	var out []IgnoreLevel
	for _, lvl := range rs.IgnoreLevels {
		if isGlobalOrAncestor(rs, lvl.Dir, full) {
			out = append(out, lvl)
		}
	}
	return out
}

func (rs *RuleSet) applicableMaskLevels(relPath string) []MaskLevel {
	full := filepath.Join(rs.Root, relPath)
	var out []MaskLevel
	for _, lvl := range rs.MaskLevels {
		if isGlobalOrAncestor(rs, lvl.Dir, full) {
			out = append(out, lvl)
		}
	}
	return out
}

func isGlobalOrAncestor(rs *RuleSet, levelDir, full string) bool {
	if rs.GlobalDir != "" && levelDir == rs.GlobalDir {
		return true
	}
	if levelDir == full {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(full, levelDir+sep)
}
