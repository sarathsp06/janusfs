package rules

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

// TraceEntry records one rule's contribution to a Resolve call, for
// janusfs explain and janusfs check.
type TraceEntry struct {
	File    string
	LineNo  int
	Kind    string // "ignore" | "mask" | "config_error"
	Line    string
	Matched bool
	Negated bool
}

// Resolution is the resolved state of one path plus the evidence behind it. The
// Trace field carries the derivation, which is what lets `janusfs explain` show
// how a decision was reached rather than just the verdict.
type Resolution struct {
	Decision     Decision
	RuleRef      string // "<file>:<line>" of the deciding rule, "" if none matched
	PatternNames []string
	// Patterns holds the same deduplicated set as PatternNames, but as the
	// actual compiled *patterns.Pattern objects (§8.1's CompiledPattern) —
	// internal/provider needs these to redact, not just their names.
	// Parallel to PatternNames: Patterns[i].Name == PatternNames[i].
	Patterns []*patterns.Pattern
	Poisoned bool // true if a config error, not a rule, forced this to Hidden
	Trace    []TraceEntry
}

// Resolve applies the precedence Hidden > Masked > Allowed to relPath (relative
// to rs.Root), including the ancestor short-circuit: a Hidden ancestor directory
// forces every descendant Hidden regardless of deeper rules. It never errors —
// any internal inconsistency, such as a path outside Root, folds to Hidden.
//
// Directories are never Masked: for isDir==true this only ever returns Allowed
// or Hidden.
func (rs *RuleSet) Resolve(relPath string, isDir bool) Resolution {
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	if relPath == "." {
		relPath = ""
	}

	// In-place zero-allocation ancestor loop
	for i := 0; i < len(relPath); i++ {
		if relPath[i] == '/' {
			ancestor := relPath[:i]
			if hidden, ref, poisoned, trace := rs.resolveIgnore(ancestor, true); hidden {
				return Resolution{Decision: Hidden, RuleRef: ref, Poisoned: poisoned, Trace: trace}
			}
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

	for i := range rs.MaskLevels {
		lvl := &rs.MaskLevels[i]
		if !lvl.isApplicable(relPath) {
			continue
		}
		relToLevel := lvl.relativeTo(rs, relPath)

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
// gitignore's "later match wins", with one precedence change on top: the
// global level is a fail-closed floor. Once the global level's own
// (self-consistent) evaluation decides a path is Hidden, no in-tree level
// (mount root or any subdirectory) may negate that verdict — negation
// still works exactly as before *within* the in-tree tier (a deeper
// in-tree file can still override a shallower in-tree file), and *within*
// the global level itself (a later global line can still override an
// earlier one there). Only an in-tree actor lifting a global verdict is
// blocked, because in-tree config files live inside the tree the agent can see.
// This is the same cross-trust-boundary property that makes config files
// read-only through the mount.
//
// A poisoned level (a line that failed to compile) forces Hidden for every
// path it would otherwise cover, per IgnoreLevel.Poisoned's doc; the
// poisoned return lets Resolve set Resolution.Poisoned regardless
// of which tier the poisoned level belongs to.
func (rs *RuleSet) resolveIgnore(relPath string, isDir bool) (hidden bool, ruleRef string, poisoned bool, trace []TraceEntry) {
	floorHidden := false

	for i := range rs.IgnoreLevels {
		lvl := &rs.IgnoreLevels[i]
		if !lvl.isApplicable(relPath) {
			continue
		}
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

		relToLevel := lvl.relativeTo(rs, relPath)
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

func (lvl *IgnoreLevel) isApplicable(relPath string) bool {
	if lvl.IsGlobal {
		return true
	}
	if lvl.RelDir == "" {
		return true
	}
	if lvl.RelDir == relPath {
		return true
	}
	return strings.HasPrefix(relPath, lvl.RelDir+"/")
}

func (lvl *MaskLevel) isApplicable(relPath string) bool {
	if lvl.IsGlobal {
		return true
	}
	if lvl.RelDir == "" {
		return true
	}
	if lvl.RelDir == relPath {
		return true
	}
	return strings.HasPrefix(relPath, lvl.RelDir+"/")
}

func (lvl *IgnoreLevel) relativeTo(rs *RuleSet, relPath string) string {
	if lvl.IsGlobal {
		return relativeToLevel(rs.Root, lvl.Dir, relPath)
	}
	if lvl.RelDir == "" {
		return relPath
	}
	if lvl.RelDir == relPath {
		return "."
	}
	return relPath[len(lvl.RelDir)+1:]
}

func (lvl *MaskLevel) relativeTo(rs *RuleSet, relPath string) string {
	if lvl.IsGlobal {
		return relativeToLevel(rs.Root, lvl.Dir, relPath)
	}
	if lvl.RelDir == "" {
		return relPath
	}
	if lvl.RelDir == relPath {
		return "."
	}
	return relPath[len(lvl.RelDir)+1:]
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
