// Package rules discovers, parses, and compiles .janusignore/.janusmask
// files (SPEC.md §2, FR-12..FR-17) into a RuleSet that Resolve (resolve.go)
// evaluates paths against.
//
// Discovery is hierarchical (FR-15): a global level at GlobalDir()
// (docs/SPEC_AMENDMENTS.md 2026-07-17 — not in SPEC.md itself, added as the
// lowest-precedence level so a user can set machine-wide defaults) plus
// every directory under a mount root that contains either file. Gitignore
// semantics (FR-12) are implemented directly in glob.go rather than via a
// third-party library — see docs/SPEC_AMENDMENTS.md (2026-07-17,
// "gitignore matcher") for why the originally-planned
// github.com/sabhiram/go-gitignore was dropped.
package rules

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

// IgnoreFileName and MaskFileName are the two config file names discovered
// at every level (SPEC §2).
const (
	IgnoreFileName = ".janusignore"
	MaskFileName   = ".janusmask"
)

// GlobalDir returns ~/.janusfs/config, the machine-wide rule directory
// added by docs/SPEC_AMENDMENTS.md (2026-07-17). It mirrors the existing
// ~/.janusfs/run (pidfiles, FR-3) and ~/.janusfs/history (FR-42)
// conventions: one root, one subdirectory per concern.
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("rules: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "config"), nil
}

// Decision is the resolved state of a path (SPEC §2, FR-5).
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

// IgnoreLevel is one directory's compiled .janusignore (FR-12).
type IgnoreLevel struct {
	Dir      string // absolute directory this level applies to and below
	File     string // absolute path to the .janusignore
	Patterns []*ignorePattern
	RawLines []RawLine

	// Poisoned is true if any line in this file failed to compile. FR-13's
	// "that file's whole rule set at that level fails closed" is written
	// about .janusmask, but the same fail-closed reasoning applies here:
	// since .janusignore lines only ever widen what's Hidden, a line we
	// couldn't compile could not be evaluated safely, so the conservative
	// (less-visible) reading is to fold every path this level covers to
	// Hidden rather than silently skip the broken line (see Resolve).
	Poisoned bool
	LineErrs []error
}

// RawLine is one non-blank, non-comment line of a config file, kept for
// reporting (janusfs check/explain) with its 1-based line number.
type RawLine struct {
	LineNo int
	Text   string
}

// MaskEntry is one .janusmask line (FR-13): a glob and the pattern names or
// custom regexes that apply to files it matches.
type MaskEntry struct {
	LineNo      int
	Glob        string
	GlobPattern *ignorePattern // mask globs share the same gitignore-style glob syntax as .janusignore (SPEC §2.2 examples use **; docs/SPEC_AMENDMENTS.md)
	PatternRefs []string       // raw references, e.g. "env-value" or "/regex/"
	Patterns    []*patterns.Pattern
	CompileErr  error // set if the glob or any PatternRefs entry failed to compile (FR-13 fail-closed scoping)
}

// MaskLevel is one directory's compiled .janusmask.
type MaskLevel struct {
	Dir     string
	File    string
	Entries []MaskEntry
}

// RuleSet is the immutable, compiled result of discovery (SPEC §7's
// "Compiled rule set"). Levels are ordered shallowest first; the global
// level (if present) is always index 0.
type RuleSet struct {
	Root         string
	GlobalDir    string
	IgnoreLevels []IgnoreLevel
	MaskLevels   []MaskLevel
	DiscoverErrs []error // non-fatal discovery errors (unreadable files etc.), reported by janusfs check
}

// Discover walks the global config directory and root, compiling every
// .janusignore/.janusmask found (FR-15). It never returns a nil *RuleSet:
// per-file errors are collected in DiscoverErrs and also cause that file's
// affected entries to fail closed (FR-6/FR-13), but discovery itself only
// fails outright if root cannot be walked at all.
func Discover(root string) (*RuleSet, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("rules: resolving root %q: %w", root, err)
	}

	rs := &RuleSet{Root: rootAbs}

	if gd, err := GlobalDir(); err == nil {
		rs.GlobalDir = gd
		rs.loadLevel(gd)
	}

	var dirs []string
	err = filepath.WalkDir(rootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			rs.DiscoverErrs = append(rs.DiscoverErrs, fmt.Errorf("rules: walking %q: %w", path, err))
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rules: walking root %q: %w", rootAbs, err)
	}
	sort.Strings(dirs) // ancestor paths are string prefixes of their descendants, so lexicographic sort is also shallowest-first

	for _, dir := range dirs {
		rs.loadLevel(dir)
	}

	return rs, nil
}

// loadLevel loads dir's .janusignore/.janusmask (if present) into rs.
func (rs *RuleSet) loadLevel(dir string) {
	if lvl, ok, err := loadIgnoreLevel(dir); ok {
		rs.IgnoreLevels = append(rs.IgnoreLevels, lvl)
		rs.DiscoverErrs = append(rs.DiscoverErrs, lvl.LineErrs...)
	} else if err != nil {
		rs.DiscoverErrs = append(rs.DiscoverErrs, err)
	}
	if lvl, ok, err := loadMaskLevel(dir); ok {
		rs.MaskLevels = append(rs.MaskLevels, lvl)
	} else if err != nil {
		rs.DiscoverErrs = append(rs.DiscoverErrs, err)
	}
}

func readRawLines(path string) ([]RawLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []RawLine
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		text := strings.TrimRight(sc.Text(), " \t")
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		// FR-12: "#" starts a comment unless escaped ("\#" is a literal
		// leading hash).
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, `\#`) {
			continue
		}
		lines = append(lines, RawLine{LineNo: lineNo, Text: text})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func loadIgnoreLevel(dir string) (IgnoreLevel, bool, error) {
	file := filepath.Join(dir, IgnoreFileName)
	if _, err := os.Stat(file); err != nil {
		return IgnoreLevel{}, false, nil
	}
	raw, err := readRawLines(file)
	if err != nil {
		return IgnoreLevel{}, false, fmt.Errorf("rules: reading %s: %w", file, err)
	}

	lvl := IgnoreLevel{Dir: dir, File: file, RawLines: raw}
	for _, l := range raw {
		p, err := compilePattern(l.LineNo, l.Text)
		if err != nil {
			lvl.Poisoned = true
			lvl.LineErrs = append(lvl.LineErrs, fmt.Errorf("%s:%d: %w", file, l.LineNo, err))
			continue
		}
		lvl.Patterns = append(lvl.Patterns, p)
	}
	return lvl, true, nil
}

func loadMaskLevel(dir string) (MaskLevel, bool, error) {
	file := filepath.Join(dir, MaskFileName)
	if _, err := os.Stat(file); err != nil {
		return MaskLevel{}, false, nil
	}
	raw, err := readRawLines(file)
	if err != nil {
		return MaskLevel{}, false, fmt.Errorf("rules: reading %s: %w", file, err)
	}

	lvl := MaskLevel{Dir: dir, File: file}
	for _, l := range raw {
		entry, err := parseMaskLine(l.LineNo, l.Text)
		if err != nil && entry.CompileErr == nil {
			entry.CompileErr = err
		}
		lvl.Entries = append(lvl.Entries, entry)
	}
	return lvl, true, nil
}

// parseMaskLine parses one .janusmask line per FR-13's grammar:
//
//	<file-glob> [: <pattern>[, <pattern>...]]
//
// A literal ':' in the glob is escaped as '\:'. No pattern means
// whole-file. Returns an entry with CompileErr set (but still with Glob
// populated) if the glob or any referenced pattern fails to compile, so
// callers can still report which glob is affected.
func parseMaskLine(lineNo int, line string) (MaskEntry, error) {
	globPart, patternsPart, hasColon := splitUnescapedColon(line)
	glob := strings.TrimSpace(globPart)
	glob = strings.ReplaceAll(glob, `\:`, ":")

	entry := MaskEntry{LineNo: lineNo, Glob: glob}
	if glob == "" {
		return entry, fmt.Errorf("rules: %d: empty glob", lineNo)
	}
	gp, err := compilePattern(lineNo, glob)
	if err != nil {
		return entry, fmt.Errorf("rules: %d: compiling glob %q: %w", lineNo, glob, err)
	}
	entry.GlobPattern = gp

	if !hasColon {
		ps, _ := patterns.LookupBuiltin(patterns.WholeFileName)
		entry.Patterns = ps
		entry.PatternRefs = []string{patterns.WholeFileName}
		return entry, nil
	}

	refs := splitPatternRefs(patternsPart)
	if len(refs) == 0 {
		ps, _ := patterns.LookupBuiltin(patterns.WholeFileName)
		entry.Patterns = ps
		entry.PatternRefs = []string{patterns.WholeFileName}
		return entry, nil
	}

	entry.PatternRefs = refs
	var firstErr error
	for _, ref := range refs {
		ps, err := patterns.ParsePatternRef(ref)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("rules: %d: %w", lineNo, err)
			}
			continue
		}
		entry.Patterns = append(entry.Patterns, ps...)
	}
	return entry, firstErr
}

// splitUnescapedColon splits line on the first ':' not preceded by '\'.
func splitUnescapedColon(line string) (before, after string, found bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == ':' && (i == 0 || line[i-1] != '\\') {
			return line[:i], line[i+1:], true
		}
	}
	return line, "", false
}

func splitPatternRefs(s string) []string {
	// Custom regex refs may themselves contain commas inside /.../, so split
	// on commas that are not inside a pair of slashes.
	var refs []string
	inSlash := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '/':
			inSlash = !inSlash
		case ',':
			if !inSlash {
				refs = append(refs, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	last := strings.TrimSpace(s[start:])
	if last != "" {
		refs = append(refs, last)
	}
	var out []string
	for _, r := range refs {
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}
