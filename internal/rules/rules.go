// Package rules discovers, parses, and compiles .janusfs.yml policy files
// into a RuleSet that Resolve (resolve.go)
// evaluates paths against.
//
// Discovery is hierarchical: a machine-wide global level at GlobalDir(), plus
// every directory under a mount root that contains a policy file. Gitignore
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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sarathsp06/janusfs/internal/patterns"
	"gopkg.in/yaml.v3"
)

// PolicyFileName is the config file name discovered at every level.
const PolicyFileName = ".janusfs.yml"

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

// IgnoreLevel is one directory's compiled hide/allow policy.
type IgnoreLevel struct {
	Dir      string // absolute directory this level applies to and below
	File     string // absolute path to the policy file
	Patterns []*ignorePattern
	RawLines []RawLine
	IsGlobal bool
	RelDir   string

	// Poisoned is true if any line in this file failed to compile, which folds
	// every path this level covers to Hidden. The reasoning:
	// since hide lines only ever widen what's Hidden, a line we
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

// MaskEntry is one mask rule path: a glob and the pattern names or
// custom regexes that apply to files it matches.
type MaskEntry struct {
	LineNo      int
	Glob        string
	GlobPattern *ignorePattern // mask globs use the same gitignore-style syntax as hide rules
	PatternRefs []string       // raw references, e.g. "env-value" or "/regex/"
	Patterns    []*patterns.Pattern
	CompileErr  error // set if the glob or any PatternRefs entry failed to compile; fails this entry closed
}

// MaskLevel is one directory's compiled mask policy.
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
	DiscoverErrs []error  // non-fatal discovery errors (unreadable files etc.), reported by janusfs check
	PolicyDirs   []string // directories where .janusfs.yml existed, even if it compiled to no levels

	// FoldCase is true when Root's backing volume treats two spellings of a
	// name as the same file (the APFS/HFS+ default). Every pattern in
	// IgnoreLevels and MaskLevels is compiled against this same setting, so
	// glob matching agrees with what the kernel itself would resolve.
	FoldCase bool
}

// Discover walks the global config directory and root, compiling every
// .janusfs.yml found. It never returns a nil *RuleSet:
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

// loadLevel loads dir's .janusfs.yml (if present) into rs.
func (rs *RuleSet) loadLevel(dir string) {
	if _, err := os.Stat(filepath.Join(dir, PolicyFileName)); err == nil {
		rs.PolicyDirs = append(rs.PolicyDirs, dir)
	}
	ignore, hasIgnore, mask, hasMask, errs := loadPolicyLevel(dir, rs.FoldCase)

	isGlobal := rs.GlobalDir != "" && dir == rs.GlobalDir
	var relDir string
	if !isGlobal {
		if rel, err := filepath.Rel(rs.Root, dir); err == nil {
			relDir = filepath.ToSlash(rel)
			if relDir == "." {
				relDir = ""
			}
		}
	}

	if hasIgnore {
		ignore.IsGlobal = isGlobal
		ignore.RelDir = relDir
		rs.IgnoreLevels = append(rs.IgnoreLevels, ignore)
		rs.DiscoverErrs = append(rs.DiscoverErrs, ignore.LineErrs...)
	}
	if hasMask {
		mask.IsGlobal = isGlobal
		mask.RelDir = relDir
		rs.MaskLevels = append(rs.MaskLevels, mask)
	}
	rs.DiscoverErrs = append(rs.DiscoverErrs, errs...)
}

type policyFile struct {
	Version int          `yaml:"version"`
	Hide    []string     `yaml:"hide"`
	Allow   []string     `yaml:"allow"`
	Mask    []policyMask `yaml:"mask"`
}

type policyMask struct {
	Paths    []string `yaml:"paths"`
	Patterns []string `yaml:"patterns"`
}

func loadPolicyLevel(dir string, foldCase bool) (IgnoreLevel, bool, MaskLevel, bool, []error) {
	file := filepath.Join(dir, PolicyFileName)
	if _, err := os.Stat(file); err != nil {
		return IgnoreLevel{}, false, MaskLevel{}, false, nil
	}

	data, err := os.ReadFile(file)
	if err != nil {
		lvl := IgnoreLevel{Dir: dir, File: file, Poisoned: true}
		return lvl, true, MaskLevel{}, false, []error{fmt.Errorf("rules: reading %s: %w", file, err)}
	}

	var pf policyFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		lvl := IgnoreLevel{Dir: dir, File: file, Poisoned: true}
		return lvl, true, MaskLevel{}, false, []error{fmt.Errorf("rules: parsing %s: %w", file, err)}
	}
	if pf.Version != 1 {
		lvl := IgnoreLevel{Dir: dir, File: file, Poisoned: true}
		return lvl, true, MaskLevel{}, false, []error{fmt.Errorf("rules: %s: unsupported policy version %d", file, pf.Version)}
	}

	ignore, hasIgnore := compileIgnorePolicy(file, dir, foldCase, pf.Hide, pf.Allow)
	mask, hasMask := compileMaskPolicy(file, dir, foldCase, pf.Mask)
	return ignore, hasIgnore, mask, hasMask, nil
}

func compileIgnorePolicy(file, dir string, foldCase bool, hide, allow []string) (IgnoreLevel, bool) {
	if len(hide) == 0 && len(allow) == 0 {
		return IgnoreLevel{}, false
	}
	lvl := IgnoreLevel{Dir: dir, File: file}
	lineNo := 0
	for _, glob := range hide {
		lineNo++
		addIgnorePattern(&lvl, file, lineNo, strings.TrimSpace(glob), foldCase)
	}
	for _, glob := range allow {
		lineNo++
		glob = strings.TrimSpace(glob)
		if glob != "" && !strings.HasPrefix(glob, "!") {
			glob = "!" + glob
		}
		addIgnorePattern(&lvl, file, lineNo, glob, foldCase)
	}
	return lvl, true
}

func addIgnorePattern(lvl *IgnoreLevel, file string, lineNo int, glob string, foldCase bool) {
	lvl.RawLines = append(lvl.RawLines, RawLine{LineNo: lineNo, Text: glob})
	p, err := compilePatternFold(lineNo, glob, foldCase)
	if err != nil {
		lvl.Poisoned = true
		lvl.LineErrs = append(lvl.LineErrs, fmt.Errorf("%s:%d: %w", file, lineNo, err))
		return
	}
	lvl.Patterns = append(lvl.Patterns, p)
}

func compileMaskPolicy(file, dir string, foldCase bool, masks []policyMask) (MaskLevel, bool) {
	if len(masks) == 0 {
		return MaskLevel{}, false
	}
	lvl := MaskLevel{Dir: dir, File: file}
	lineNo := 0
	for _, m := range masks {
		patternsPart := strings.Join(m.Patterns, ", ")
		for _, path := range m.Paths {
			lineNo++
			line := strings.TrimSpace(path)
			if patternsPart != "" {
				line += " : " + patternsPart
			}
			entry, err := parseMaskLine(lineNo, line, foldCase)
			if err != nil && entry.CompileErr == nil {
				entry.CompileErr = err
			}
			lvl.Entries = append(lvl.Entries, entry)
		}
	}
	return lvl, true
}

// parseMaskLine parses one synthesized mask rule line, whose grammar is:
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
