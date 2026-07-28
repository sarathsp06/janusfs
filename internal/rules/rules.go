// Package rules discovers, parses, and compiles .janusignore/.janusmask
// files into a RuleSet that Resolve (resolve.go)
// evaluates paths against.
//
// Discovery is hierarchical: a machine-wide global level at GlobalDir(), plus
// every directory under a mount root that contains either file. Gitignore
// semantics are implemented directly in glob.go rather than via a third-party
// library; that package's doc explains why.
//
// Precedence (resolve.go) is a two-tier floor per the 2026-07-18 amendment:
// the global level is a fail-closed floor — no in-tree rule (mount root or
// any subdirectory) may negate a Hidden/Masked verdict the global level set.
// Within the in-tree tier, gitignore's own deeper-wins and negation precedence
// is unchanged.
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
// at every level.
const (
	IgnoreFileName = ".janusignore"
	MaskFileName   = ".janusmask"
)

// GlobalDir returns ~/.janusfs/config, the machine-wide rule directory. It sits
// outside every source tree, which is what makes it a floor an in-tree rule
// cannot negate. Layout mirrors ~/.janusfs/run and ~/.janusfs/history: one root,
// one subdirectory per concern.
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("rules: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "config"), nil
}

// Decision is the resolved state of a path.
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

// IgnoreLevel is one directory's compiled .janusignore.
type IgnoreLevel struct {
	Dir      string // absolute directory this level applies to and below
	File     string // absolute path to the .janusignore
	Patterns []*ignorePattern
	RawLines []RawLine

	// Poisoned is true if any line in this file failed to compile, which folds
	// every path this level covers to Hidden. The reasoning:
	// since .janusignore lines only ever widen what's Hidden, a line we
	// couldn't compile could not be evaluated safely, so the conservative
	// (less-visible) reading is to fold every path this level covers to
	// Hidden rather than silently skip the broken line (see Resolve).
	Poisoned bool
	LineErrs []error

	IsGlobal bool
	RelDir   string
}

// RawLine is one non-blank, non-comment line of a config file, kept for
// reporting (janusfs check/explain) with its 1-based line number.
type RawLine struct {
	LineNo int
	Text   string
}

// MaskEntry is one .janusmask line: a glob and the pattern names or
// custom regexes that apply to files it matches.
type MaskEntry struct {
	LineNo      int
	Glob        string
	GlobPattern *ignorePattern // mask globs use the same gitignore-style syntax as .janusignore
	PatternRefs []string       // raw references, e.g. "env-value" or "/regex/"
	Patterns    []*patterns.Pattern
	CompileErr  error // set if the glob or any PatternRefs entry failed to compile; fails this entry closed
}

// MaskLevel is one directory's compiled .janusmask.
type MaskLevel struct {
	Dir      string
	File     string
	Entries  []MaskEntry
	IsGlobal bool
	RelDir   string
}

// RuleSet is the immutable, compiled result of discovery. Levels are ordered
// shallowest first; the global level, if present, is always index 0.
type RuleSet struct {
	Root         string
	GlobalDir    string
	IgnoreLevels []IgnoreLevel
	MaskLevels   []MaskLevel
	DiscoverErrs []error // non-fatal discovery errors (unreadable files etc.), reported by janusfs check

	// FoldCase is true when Root's backing volume treats two spellings of a
	// name as the same file (the APFS/HFS+ default). Every pattern in
	// IgnoreLevels and MaskLevels is compiled against this same setting, so
	// glob matching agrees with what the kernel itself would resolve.
	FoldCase bool
}

// Discover walks the global config directory and root, compiling every
// .janusignore/.janusmask found. It never returns a nil *RuleSet:
// per-file errors are collected in DiscoverErrs and also cause that file's
// affected entries to fail closed, but discovery itself only
// fails outright if root cannot be walked at all.
func Discover(root string) (*RuleSet, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("rules: resolving root %q: %w", root, err)
	}

	rs := &RuleSet{Root: rootAbs, FoldCase: caseInsensitiveVolume(rootAbs)}

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
	isGlobal := rs.GlobalDir != "" && dir == rs.GlobalDir
	var relDir string
	if !isGlobal {
		rel, err := filepath.Rel(rs.Root, dir)
		if err == nil {
			relDir = filepath.ToSlash(rel)
			if relDir == "." {
				relDir = ""
			}
		}
	}

	if lvl, ok, err := loadIgnoreLevel(dir, rs.FoldCase); ok {
		lvl.IsGlobal = isGlobal
		lvl.RelDir = relDir
		rs.IgnoreLevels = append(rs.IgnoreLevels, lvl)
		rs.DiscoverErrs = append(rs.DiscoverErrs, lvl.LineErrs...)
	} else if err != nil {
		rs.DiscoverErrs = append(rs.DiscoverErrs, err)
	}
	if lvl, ok, err := loadMaskLevel(dir, rs.FoldCase); ok {
		lvl.IsGlobal = isGlobal
		lvl.RelDir = relDir
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
	defer func() { _ = f.Close() }()

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
		// "#" starts a comment unless escaped: "\#" is a literal leading hash.
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

func loadIgnoreLevel(dir string, foldCase bool) (IgnoreLevel, bool, error) {
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
		p, err := compilePatternFold(l.LineNo, l.Text, foldCase)
		if err != nil {
			lvl.Poisoned = true
			lvl.LineErrs = append(lvl.LineErrs, fmt.Errorf("%s:%d: %w", file, l.LineNo, err))
			continue
		}
		lvl.Patterns = append(lvl.Patterns, p)
	}
	return lvl, true, nil
}

func loadMaskLevel(dir string, foldCase bool) (MaskLevel, bool, error) {
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
		entry, err := parseMaskLine(l.LineNo, l.Text, foldCase)
		if err != nil && entry.CompileErr == nil {
			entry.CompileErr = err
		}
		lvl.Entries = append(lvl.Entries, entry)
	}
	return lvl, true, nil
}

// parseMaskLine parses one .janusmask line, whose grammar is:
//
//	<file-glob> [: <pattern>[, <pattern>...]]
//
// A literal ':' in the glob is escaped as '\:'. No pattern means
// whole-file. Returns an entry with CompileErr set (but still with Glob
// populated) if the glob or any referenced pattern fails to compile, so
// callers can still report which glob is affected.
func parseMaskLine(lineNo int, line string, foldCase bool) (MaskEntry, error) {
	line = stripMaskInlineComment(line)
	globPart, patternsPart, hasColon := splitUnescapedColon(line)
	glob := strings.TrimSpace(globPart)
	glob = strings.ReplaceAll(glob, `\:`, ":")

	entry := MaskEntry{LineNo: lineNo, Glob: glob}
	if glob == "" {
		return entry, fmt.Errorf("rules: %d: empty glob", lineNo)
	}
	gp, err := compilePatternFold(lineNo, glob, foldCase)
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

func stripMaskInlineComment(line string) string {
	inSlash := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '/':
			if i == 0 || line[i-1] != '\\' {
				inSlash = !inSlash
			}
		case '#':
			if !inSlash && (i == 0 || line[i-1] != '\\') {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return line
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
