// Package check is the static config linter: it walks a rule
// tree (internal/rules) and a real source tree together and reports
// findings a human should look at before mounting — regex/glob errors,
// mask globs that accidentally target a directory, negations that can never
// take effect because an
// ancestor directory is Hidden, and in-tree negations that can never
// take effect because the global rule floor already hid the path.
//
// This package is shared by both `janusfs check` and the
// `.janusfs/conflicts.json` virtual file, so it has no CLI or FUSE dependencies
// of its own.
package check

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/patterns"
	"github.com/sarathsp06/janusfs/internal/rules"
)

// Severity orders findings for display, worst first.
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
// rather than a bare int, so --json output is self-describing and a consumer
// need not know this package's internal enum ordering.
func (s Severity) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// Finding is one static-analysis result: file:line, a severity, a
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

// HasErrors reports whether any finding is SeverityError, which is what makes
// `janusfs check` exit 1.
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

// Options controls optional checks that are intentionally not part of the
// default linter. Defaults stay quiet and syntax/intent-focused; opt-ins may
// use heuristics that are useful before handing a tree to an agent but should
// not be mistaken for proof of complete coverage.
type Options struct {
	// Secrets enables a conservative best-effort scan for files that look like
	// secrets but currently resolve Allowed. It reports warnings, never errors.
	Secrets bool
}

// Run discovers root's rule tree and lints it against root's real contents.
func Run(root string) (Report, error) {
	return RunWithOptions(root, Options{})
}

// RunWithOptions discovers root's rule tree and lints it against root's real
// contents, enabling optional heuristic checks requested by opts.
func RunWithOptions(root string, opts Options) (Report, error) {
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
	findings = append(findings, maskLevelFindings(rs, entries)...)
	findings = append(findings, hiddenDirNegationFindings(rs, eng, entries)...)
	findings = append(findings, globalFloorNegationFindings(rs, eng, entries)...)
	if opts.Secrets {
		findings = append(findings, secretFindings(rootAbs, eng, entries)...)
	}
	findings = append(findings, redundancyFindings(rs)...)
	findings = append(findings, maskRedundancyFindings(rs)...)
	findings = append(findings, subdirLevelRedundancyFindings(rs, entries)...)

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

const secretScanMaxBytes = 1 << 20

func secretFindings(root string, eng *engine.Engine, entries []treeEntry) []Finding {
	var out []Finding
	contentPatterns := secretContentPatterns()
	for _, te := range entries {
		if te.isDir || eng.Resolve(te.rel, false).Decision != engine.Allowed {
			continue
		}

		if reason := secretFilenameReason(te.rel); reason != "" {
			out = append(out, Finding{
				Severity:   SeverityWarn,
				File:       filepath.Join(root, filepath.FromSlash(te.rel)),
				Message:    fmt.Sprintf("likely secret file %s is currently Allowed (%s)", te.rel, reason),
				Suggestion: "add a .janusignore rule to hide it, or a .janusmask rule if the file is useful after redaction",
			})
			continue
		}

		if reason := secretContentReason(filepath.Join(root, filepath.FromSlash(te.rel)), contentPatterns); reason != "" {
			out = append(out, Finding{
				Severity:   SeverityWarn,
				File:       filepath.Join(root, filepath.FromSlash(te.rel)),
				Message:    fmt.Sprintf("likely secret content in %s is currently Allowed (%s)", te.rel, reason),
				Suggestion: "add a .janusmask rule for this file if agents need structure, or .janusignore if they do not need it",
			})
		}
	}
	return out
}

func secretFilenameReason(rel string) string {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(rel)))
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "env file name"
	case base == ".npmrc":
		return "npm credential file name"
	case base == "credentials":
		return "credential file name"
	case base == "id_rsa" || strings.HasPrefix(base, "id_rsa."):
		return "private key file name"
	case strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx"):
		return "key/certificate file extension"
	case strings.HasSuffix(base, ".tfvars"):
		return "Terraform variables file extension"
	case strings.Contains(base, "credential"):
		return "credential-like file name"
	default:
		return ""
	}
}

func secretContentPatterns() []*patterns.Pattern {
	names := []string{"aws-key", "private-key", "jwt", "db-uri", "github-token", "generic-secret"}
	var out []*patterns.Pattern
	for _, name := range names {
		if ps, ok := patterns.LookupBuiltin(name); ok {
			out = append(out, ps...)
		}
	}
	return out
}

func secretContentReason(path string, pats []*patterns.Pattern) string {
	buf, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(buf) > secretScanMaxBytes {
		buf = buf[:secretScanMaxBytes]
	}
	for _, p := range pats {
		if p.Regex == nil {
			continue
		}
		if p.PreFilter != nil && !p.PreFilter(buf) {
			continue
		}
		if p.Regex.Match(buf) {
			return "matches " + p.Name
		}
	}
	return ""
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

// maskLevelFindings reports the two mask problems that indicate a real
// mistake: a pattern that failed to compile (unknown builtin / bad regex),
// and a glob that targets a directory (which can never be Masked). It does
// NOT flag a glob that merely matches no files today — a defensive rule
// covering files that don't exist yet is intended, not a bug.
func maskLevelFindings(rs *rules.RuleSet, entries []treeEntry) []Finding {
	var out []Finding
	for _, lvl := range rs.MaskLevels {
		for _, e := range lvl.Entries {
			if e.CompileErr != nil {
				// State the consequence, not just the parse error: a mask
				// entry that fails to compile fails closed — every file its
				// glob matches resolves to Hidden until the rule is fixed
				// (see internal/rules Resolve's poisonedMask path). Surfacing
				// that here is what makes the fail-closed behaviour legible
				// rather than a silent surprise.
				out = append(out, Finding{
					Severity: SeverityError,
					File:     lvl.File,
					Line:     e.LineNo,
					Message:  fmt.Sprintf("invalid mask rule %q — files it matches are Hidden (fail-closed) until this is fixed: %v", e.Glob, e.CompileErr),
				})
				continue
			}

			matchedDir := false
			for _, te := range entries {
				if !te.isDir || !isUnderDir(rs.Root, lvl.Dir, te.rel) {
					continue
				}
				if e.GlobPattern.Matches(relToDir(rs.Root, lvl.Dir, te.rel), true) {
					matchedDir = true
					break
				}
			}
			if matchedDir {
				// Severity is Warn, not Error: this is already
				// harmless at runtime (Resolve never evaluates mask
				// entries for isDir==true, so a directory-matching glob
				// can never actually mask anything) — this is a
				// clarity/intent flag, not a security or correctness bug.
				out = append(out, Finding{
					Severity:   SeverityWarn,
					File:       lvl.File,
					Line:       e.LineNo,
					Message:    fmt.Sprintf("mask glob %q also matches a directory, which can never be Masked — the directory match is a harmless no-op", e.Glob),
					Suggestion: fmt.Sprintf("rewrite to %q if you meant to mask only the files inside", e.Glob+"/**"),
				})
			}
		}
	}
	return out
}

// hiddenDirNegationFindings reports negations a hidden ancestor directory
// blocks: a negate pattern that matches a real file
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
							Message:  fmt.Sprintf("negation %q has no effect: %s is under a Hidden ancestor directory", p.Raw(), te.rel),
						})
					}
				}
			}
		}
	}
	return out
}

// globalFloorNegationFindings reports in-tree negations the global floor blocks:
// an in-tree (mount root or subdirectory) negation that matches a real file
// the global rule level already resolves Hidden has no effect —
// rules.RuleSet.Resolve's fail-closed floor keeps the path Hidden
// regardless of the in-tree negation. Distinct from hiddenDirNegationFindings,
// which is an ancestor-directory block: this is the cross-trust-boundary block
// between the global level and everything inside <src>.
func globalFloorNegationFindings(rs *rules.RuleSet, eng *engine.Engine, entries []treeEntry) []Finding {
	if rs.GlobalDir == "" {
		return nil
	}
	var out []Finding
	for _, lvl := range rs.IgnoreLevels {
		if lvl.Poisoned || lvl.Dir == rs.GlobalDir {
			continue // only in-tree levels can attempt to lift the floor
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
				res := eng.Resolve(te.rel, te.isDir)
				for _, t := range res.Trace {
					if t.Kind == "ignore" && t.Negated && !t.Matched && t.File == lvl.File && t.LineNo == p.LineNo() {
						out = append(out, Finding{
							Severity: SeverityWarn,
							File:     lvl.File,
							Line:     p.LineNo(),
							Message:  fmt.Sprintf("negation %q has no effect: %s is Hidden by the global rule floor", p.Raw(), te.rel),
						})
						break
					}
				}
			}
		}
	}
	return out
}

// redundancyFindings reports exact-duplicate lines across levels — a rough
// but honest approximation of "redundant pairs". True semantic-equivalence
// detection — two differently worded globs matching the same set — is left to a
// human reviewer.
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

// maskRedundancyFindings reports duplicate mask entries (identical glob +
// same pattern set at the same or different levels).
func maskRedundancyFindings(rs *rules.RuleSet) []Finding {
	seen := map[string]string{}
	var out []Finding
	for _, lvl := range rs.MaskLevels {
		for _, e := range lvl.Entries {
			if e.CompileErr != nil {
				continue
			}
			key := e.Glob
			for _, p := range e.PatternRefs {
				key += ":" + p
			}
			loc := fmt.Sprintf("%s:%d", lvl.File, e.LineNo)
			if prior, ok := seen[key]; ok {
				out = append(out, Finding{
					Severity: SeverityInfo,
					File:     lvl.File,
					Line:     e.LineNo,
					Message:  fmt.Sprintf("mask entry %q identical to %s — likely redundant", e.Glob, prior),
				})
				continue
			}
			seen[key] = loc
		}
	}
	return out
}

// subdirLevelRedundancyFindings reports subdirectory ignore levels whose
// patterns are entirely redundant because a parent level already hides all
// matched files (the subdir level can never add new hidden entries).
func subdirLevelRedundancyFindings(rs *rules.RuleSet, entries []treeEntry) []Finding {
	var levels []levelInfo
	for i, lvl := range rs.IgnoreLevels {
		if lvl.Poisoned || lvl.Dir == rs.Root || lvl.Dir == rs.GlobalDir {
			continue
		}
		var pats []matcher
		for _, p := range lvl.Patterns {
			pats = append(pats, p)
		}
		levels = append(levels, levelInfo{
			index:    i,
			dir:      lvl.Dir,
			patterns: pats,
			file:     lvl.File,
		})
	}

	if len(levels) < 2 {
		return nil
	}

	var out []Finding
	for _, child := range levels {
		parent := findParentLevel(rs.Root, child.dir, levels)
		if parent == nil {
			continue
		}
		// Check if every file the child could match is already matched by
		// the parent. If all of the child's patterns are covered, the child
		// level is redundant.
		allCovered := true
		for _, pat := range child.patterns {
			found := false
			for _, te := range entries {
				if !isUnderDir(rs.Root, child.dir, te.rel) {
					continue
				}
				if pat.Matches(relToDir(rs.Root, child.dir, te.rel), te.isDir) {
					// This file is matched by the child. Check if parent also matches it.
					parentMatch := false
					for _, pp := range parent.patterns {
						if pp.Matches(relToDir(rs.Root, parent.dir, te.rel), te.isDir) {
							parentMatch = true
							break
						}
					}
					if !parentMatch {
						found = true
						break
					}
				}
			}
			if found {
				allCovered = false
				break
			}
		}
		if allCovered && len(child.patterns) > 0 {
			out = append(out, Finding{
				Severity: SeverityInfo,
				File:     child.file,
				Message:  fmt.Sprintf("level at %s is redundant — parent level covers all matched paths", displayDir(rs.Root, child.dir)),
			})
		}
	}

	return out
}

func findParentLevel(root string, childDir string, levels []levelInfo) *levelInfo {
	var best *levelInfo
	for i, l := range levels {
		if l.dir == childDir {
			continue
		}
		// l is a parent of childDir if childDir is under l.dir.
		rel, err := filepath.Rel(l.dir, childDir)
		if err != nil {
			continue
		}
		if len(rel) > 0 && !strings.HasPrefix(rel, "..") {
			if best == nil || len(l.dir) > len(best.dir) {
				best = &levels[i]
			}
		}
	}
	return best
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

// levelInfo carries a single ignore level's metadata for redundancy analysis.
type levelInfo struct {
	index    int
	dir      string
	patterns []matcher
	file     string
}

// matcher is satisfied by internal/rules's unexported ignorePattern type
// (via its exported Matches method) — this package never names that type,
// only calls methods on values it receives through IgnoreLevel.Patterns /
// MaskEntry.GlobPattern.
type matcher interface {
	Matches(relPath string, isDir bool) bool
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
