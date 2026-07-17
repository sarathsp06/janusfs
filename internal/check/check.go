// Package check implements FR-28's static config linter: it walks a rule
// tree (internal/rules) and a real source tree together and reports
// findings a human should look at before mounting — regex/glob errors,
// globs that touch nothing, mask globs that accidentally target a
// directory (FR-9), and negations that can never take effect because an
// ancestor directory is Hidden (FR-8).
//
// This package is shared, per SPEC.md §3.7/§20.3 (Phase 5), by both
// `janusfs check` and the future `.janusfs/conflicts.json` virtual file —
// it has no CLI or FUSE dependencies of its own.
package check

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/rules"
)

// Severity orders findings for display (FR-33: "sorted by severity").
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarn
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarn:
		return "warn"
	default:
		return "info"
	}
}

// MarshalJSON renders Severity as its string form ("error"/"warn"/"info")
// rather than a bare int, so --json output is self-describing (FR-28's
// "--json for machine output" shouldn't require the reader to know this
// package's internal enum ordering).
func (s Severity) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// Finding is one FR-28 static-analysis result: file:line, a severity, a
// human-readable message, and (where applicable) a suggested fix.
type Finding struct {
	Severity   Severity `json:"severity"`
	File       string   `json:"file"`
	Line       int      `json:"line,omitempty"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// Report is the full result of Run: findings plus the counted tree size,
// so callers (and janusfs check's human-readable printer) can say "0 of
// 400 files affected" style context.
type Report struct {
	Findings  []Finding `json:"findings"`
	FileCount int       `json:"fileCount"`
	DirCount  int       `json:"dirCount"`
}

// HasErrors reports whether any finding is SeverityError — FR-28's "exit 1
// on errors, 0 otherwise."
func (r Report) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// treeEntry is one real file or directory under root, relative-path plus
// dir-ness, collected once and reused for every glob's zero-match and
// directory-match checks (so Run stays a single tree walk regardless of
// how many rules exist).
type treeEntry struct {
	rel   string
	isDir bool
}

// Run discovers root's rule tree and lints it against root's real
// contents (FR-28).
func Run(root string) (Report, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("check: resolving root %q: %w", root, err)
	}

	rs, err := rules.Discover(rootAbs)
	if err != nil {
		return Report{}, err
	}

	eng, err := engine.New(rootAbs)
	if err != nil {
		return Report{}, err
	}

	entries, err := walkTree(rootAbs)
	if err != nil {
		return Report{}, err
	}

	var findings []Finding
	findings = append(findings, discoverErrorFindings(rs)...)
	findings = append(findings, ignoreLevelFindings(rs, entries)...)
	findings = append(findings, maskLevelFindings(rs, entries)...)
	findings = append(findings, hiddenDirNegationFindings(rs, eng, entries)...)
	findings = append(findings, redundancyFindings(rs)...)

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity > findings[j].Severity // errors first
		}
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})

	fileCount, dirCount := 0, 0
	for _, e := range entries {
		if e.isDir {
			dirCount++
		} else {
			fileCount++
		}
	}

	return Report{Findings: findings, FileCount: fileCount, DirCount: dirCount}, nil
}

func walkTree(root string) ([]treeEntry, error) {
	var entries []treeEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort walk; unreadable entries are simply excluded from lint coverage
		}
		if path == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		entries = append(entries, treeEntry{rel: filepath.ToSlash(rel), isDir: d.IsDir()})
		return nil
	})
	return entries, err
}

// discoverErrorFindings surfaces rules.RuleSet.DiscoverErrs (unreadable
// files, malformed lines) as SeverityError findings.
func discoverErrorFindings(rs *rules.RuleSet) []Finding {
	var out []Finding
	for _, err := range rs.DiscoverErrs {
		out = append(out, Finding{Severity: SeverityError, File: rs.Root, Message: err.Error()})
	}
	return out
}

// ignoreLevelFindings reports zero-match .janusignore lines (FR-28).
func ignoreLevelFindings(rs *rules.RuleSet, entries []treeEntry) []Finding {
	var out []Finding
	for _, lvl := range rs.IgnoreLevels {
		if lvl.Poisoned {
			continue // already reported via DiscoverErrs
		}
		for _, p := range lvl.Patterns {
			if !matchesAnyEntry(lvl.Dir, rs.Root, p, entries) {
				out = append(out, Finding{
					Severity: SeverityWarn,
					File:     lvl.File,
					Line:     p.LineNo(),
					Message:  fmt.Sprintf("pattern %q matches no files under %s", p.Raw(), displayDir(rs.Root, lvl.Dir)),
				})
			}
		}
	}
	return out
}

// maskLevelFindings reports FR-9 directory-mask rewrites, zero-match
// globs, and pattern-reference errors (unknown builtin / bad regex).
func maskLevelFindings(rs *rules.RuleSet, entries []treeEntry) []Finding {
	var out []Finding
	for _, lvl := range rs.MaskLevels {
		for _, e := range lvl.Entries {
			if e.CompileErr != nil {
				out = append(out, Finding{
					Severity: SeverityError,
					File:     lvl.File,
					Line:     e.LineNo,
					Message:  e.CompileErr.Error(),
				})
				continue
			}

			matchedDir, matchedFile := false, false
			for _, te := range entries {
				if !isUnderDir(rs.Root, lvl.Dir, te.rel) {
					continue
				}
				rel := relToDir(rs.Root, lvl.Dir, te.rel)
				if e.GlobPattern.Matches(rel, te.isDir) {
					if te.isDir {
						matchedDir = true
					} else {
						matchedFile = true
					}
				}
			}

			if matchedDir {
				// Severity is Warn, not Error: FR-9 already makes this
				// harmless at runtime (Resolve never evaluates mask
				// entries for isDir==true, so a directory-matching glob
				// can never actually mask anything) — this is a
				// clarity/intent flag, not a security or correctness bug.
				out = append(out, Finding{
					Severity:   SeverityWarn,
					File:       lvl.File,
					Line:       e.LineNo,
					Message:    fmt.Sprintf("mask glob %q also matches a directory, which can never be Masked (FR-9) — the directory match is a harmless no-op", e.Glob),
					Suggestion: fmt.Sprintf("rewrite to %q if you meant to mask only the files inside", e.Glob+"/**"),
				})
			}
			if !matchedDir && !matchedFile {
				out = append(out, Finding{
					Severity: SeverityWarn,
					File:     lvl.File,
					Line:     e.LineNo,
					Message:  fmt.Sprintf("mask glob %q matches no files under %s", e.Glob, displayDir(rs.Root, lvl.Dir)),
				})
			}
		}
	}
	return out
}

// hiddenDirNegationFindings reports FR-8 "negation attempts a hidden
// ancestor directory blocks": a negate pattern that matches a real file
// whose containing directory (or a shallower ancestor) resolves Hidden.
func hiddenDirNegationFindings(rs *rules.RuleSet, eng *engine.Engine, entries []treeEntry) []Finding {
	var out []Finding
	for _, lvl := range rs.IgnoreLevels {
		if lvl.Poisoned {
			continue
		}
		for _, p := range lvl.Patterns {
			if !p.Negate() {
				continue
			}
			for _, te := range entries {
				if !isUnderDir(rs.Root, lvl.Dir, te.rel) {
					continue
				}
				rel := relToDir(rs.Root, lvl.Dir, te.rel)
				if !p.Matches(rel, te.isDir) {
					continue
				}
				if dir := filepath.ToSlash(filepath.Dir(te.rel)); dir != "." && dir != te.rel {
					if eng.Resolve(dir, true).Decision == engine.Hidden {
						out = append(out, Finding{
							Severity: SeverityWarn,
							File:     lvl.File,
							Line:     p.LineNo(),
							Message:  fmt.Sprintf("negation %q has no effect: %s is under a Hidden ancestor directory (FR-8)", p.Raw(), te.rel),
						})
					}
				}
			}
		}
	}
	return out
}

// redundancyFindings reports exact-duplicate lines across levels — a rough
// but honest approximation of FR-28's "redundant pairs (identical
// effect)": true semantic-equivalence detection (e.g. two differently
// worded globs matching the same set) is left to a human reviewer per the
// SPEC's Phase 5 diagnostics-maturity scope.
func redundancyFindings(rs *rules.RuleSet) []Finding {
	seen := map[string]string{} // raw text -> "file:line" of the first sighting
	var out []Finding
	for _, lvl := range rs.IgnoreLevels {
		for _, raw := range lvl.RawLines {
			key := raw.Text
			loc := fmt.Sprintf("%s:%d", lvl.File, raw.LineNo)
			if prior, ok := seen[key]; ok {
				out = append(out, Finding{
					Severity: SeverityInfo,
					File:     lvl.File,
					Line:     raw.LineNo,
					Message:  fmt.Sprintf("identical to %s — likely redundant", prior),
				})
				continue
			}
			seen[key] = loc
		}
	}
	return out
}

func isUnderDir(root, dir, entryRel string) bool {
	full := filepath.Join(root, entryRel)
	if dir == full {
		return true
	}
	sep := string(filepath.Separator)
	return len(full) > len(dir) && full[:len(dir)] == dir && (full[len(dir)] == sep[0])
}

func relToDir(root, dir, entryRel string) string {
	full := filepath.Join(root, entryRel)
	rel, err := filepath.Rel(dir, full)
	if err != nil {
		return entryRel
	}
	return filepath.ToSlash(rel)
}

// matcher is satisfied by internal/rules's unexported ignorePattern type
// (via its exported Matches method) — this package never names that type,
// only calls methods on values it receives through IgnoreLevel.Patterns /
// MaskEntry.GlobPattern.
type matcher interface {
	Matches(relPath string, isDir bool) bool
}

func matchesAnyEntry(dir, root string, p matcher, entries []treeEntry) bool {
	for _, te := range entries {
		if !isUnderDir(root, dir, te.rel) {
			continue
		}
		rel := relToDir(root, dir, te.rel)
		if p.Matches(rel, te.isDir) {
			return true
		}
	}
	return false
}

func displayDir(root, dir string) string {
	if dir == root {
		return "."
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return rel
}
