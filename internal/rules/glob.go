package rules

import (
	"fmt"
	"regexp"
	"strings"
)

// ignorePattern is one compiled line of a .janusignore file, or one glob
// half of a .janusmask line (FR-12/FR-13). Implemented from scratch rather
// than via a third-party gitignore library: see
// docs/SPEC_AMENDMENTS.md (2026-07-17, "gitignore matcher") for why — in
// short, the previously-vendored github.com/sabhiram/go-gitignore is
// low-activity (165 stars, no commits since Feb 2024) and its per-file
// negation model doesn't compose correctly across the hierarchical levels
// FR-12 requires (a deeper file's negation must be able to re-include a
// path a shallower file excluded; that library evaluates each file in
// isolation and can't express that). Precedence across levels is handled by
// evaluating one ordered, cross-level pattern list (see resolve.go), so
// this type only needs to compile and match a single line correctly.
//
// docs/SPEC_AMENDMENTS.md (2026-07-18, "Rule precedence") considered and
// withdrew replacing this matcher with bmatcuk/doublestar + a
// no-negation model: gitignore's "no-slash pattern matches at every depth"
// rule is the fail-closed default a secret-hiding tool wants, and a
// per-level doublestar match would have silently under-matched nested
// files instead. That amendment kept this matcher and gitignore semantics
// exactly as documented below, and added one precedence change on top (see
// resolve.go's global-tier floor) rather than replacing the matcher.
//
// Semantics match the gitignore rules summarized in SPEC.md §2.1/FR-12,
// cross-checked against real `git check-ignore` (see glob_test.go's
// conformance cases).
type ignorePattern struct {
	negate  bool
	dirOnly bool
	re      *regexp.Regexp
	lineNo  int
	raw     string // original line, unmodified, for reporting
}

// compilePattern parses and compiles one non-blank, non-comment line (a
// .janusignore line, or a .janusmask glob) into an ignorePattern.
func compilePattern(lineNo int, raw string) (*ignorePattern, error) {
	line := raw

	negate := false
	switch {
	case strings.HasPrefix(line, `\!`):
		line = "!" + line[2:]
	case strings.HasPrefix(line, "!"):
		negate = true
		line = line[1:]
	}

	if strings.HasPrefix(line, `\#`) {
		line = "#" + line[2:]
	}

	dirOnly := false
	if strings.HasSuffix(line, "/") {
		dirOnly = true
		line = line[:len(line)-1]
	}

	anchored := false
	if strings.HasPrefix(line, "/") {
		anchored = true
		line = line[1:]
	}

	if line == "" {
		return nil, fmt.Errorf("rules: line %d: empty pattern after stripping anchors", lineNo)
	}

	hasInternalSlash := strings.Contains(line, "/")
	core, err := translateGlob(line)
	if err != nil {
		return nil, fmt.Errorf("rules: line %d: %w", lineNo, err)
	}

	var full string
	if anchored || hasInternalSlash {
		// FR-12: a pattern with a slash anywhere but a trailing one is
		// relative only to its own directory, not re-checked at every
		// depth (verified against `git check-ignore`: "config/x.yaml" in a
		// root .gitignore does NOT match "sub/config/x.yaml").
		full = "^" + core + "$"
	} else {
		// A slash-free pattern matches its target at any depth under the
		// pattern's own directory (SPEC §2.1 rule 6).
		full = "^(?:.*/)?" + core + "$"
	}

	re, err := regexp.Compile(full)
	if err != nil {
		return nil, fmt.Errorf("rules: line %d: compiling %q: %w", lineNo, raw, err)
	}
	return &ignorePattern{negate: negate, dirOnly: dirOnly, re: re, lineNo: lineNo, raw: raw}, nil
}

// matches reports whether relPath (slash-separated, relative to this
// pattern's own directory, no leading/trailing slash) is targeted by p.
// dirOnly patterns only ever match a directory being tested as itself
// (FR-9's "directories aren't masked" and FR-12's trailing-slash rule);
// they are not expected to independently express "and everything beneath
// it" — that is the ancestor short-circuit's job (Resolve, FR-8).
func (p *ignorePattern) matches(relPath string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return p.re.MatchString(relPath)
}

// Matches is matches's exported form, for consumers outside this package
// that hold a pattern value via IgnoreLevel.Patterns or MaskEntry.GlobPattern
// (internal/check's static lint, SPEC FR-28) without needing to name the
// unexported ignorePattern type themselves.
func (p *ignorePattern) Matches(relPath string, isDir bool) bool { return p.matches(relPath, isDir) }

// LineNo returns the 1-based source line this pattern was compiled from.
func (p *ignorePattern) LineNo() int { return p.lineNo }

// Negate reports whether this is a "!"-negation pattern (FR-12).
func (p *ignorePattern) Negate() bool { return p.negate }

// Raw returns the original, unmodified source line.
func (p *ignorePattern) Raw() string { return p.raw }

// translateGlob compiles one anchor-stripped, dir-marker-stripped gitignore
// glob into a regex fragment matching a single relative path (using '/' as
// the component separator). It supports '*', '?', '[...]' character
// classes, and the four '**' forms (SPEC §2.1 rule 9): bare "**", leading
// "**/", trailing "/**", and interior "/**/ ".
func translateGlob(pattern string) (string, error) {
	segs := strings.Split(pattern, "/")

	pieces := make([]string, len(segs))
	isStar := make([]bool, len(segs))
	for i, seg := range segs {
		if seg == "**" {
			isStar[i] = true
			continue
		}
		p, err := translateSegment(seg)
		if err != nil {
			return "", err
		}
		pieces[i] = p
	}

	var b strings.Builder
	for i := 0; i < len(segs); i++ {
		if isStar[i] {
			if i > 0 && isStar[i-1] {
				continue // collapse consecutive "**" (git treats extras as invalid; we just no-op them)
			}
			switch {
			case len(segs) == 1:
				b.WriteString(".*")
			case i == 0:
				b.WriteString("(?:.*/)?")
			case i == len(segs)-1:
				b.WriteString("/.*")
			default:
				b.WriteString("(?:/.*)?")
			}
			continue
		}

		if i > 0 && !isStar[i-1] {
			b.WriteString("/")
		}
		b.WriteString(pieces[i])
	}
	return b.String(), nil
}

// translateSegment compiles one path component (no '/', no '**') containing
// only literal characters, '*', '?', and '[...]' classes into a regex
// fragment that cannot itself cross a '/' boundary.
func translateSegment(seg string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(seg) {
		c := seg[i]
		switch c {
		case '*':
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			end := strings.IndexByte(seg[i+1:], ']')
			if end < 0 {
				return "", fmt.Errorf("unterminated character class in %q", seg)
			}
			end = i + 1 + end
			class := seg[i : end+1] // includes brackets
			b.WriteString(translateClass(class))
			i = end + 1
		case '.', '+', '(', ')', '^', '$', '|', '\\', '{', '}':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// translateClass passes a gitignore/glob character class through to RE2
// mostly verbatim (both use POSIX-ish class syntax), translating only the
// leading negation marker: glob uses '!' or '^', RE2 only understands '^'.
func translateClass(class string) string {
	inner := class[1 : len(class)-1]
	if strings.HasPrefix(inner, "!") {
		inner = "^" + inner[1:]
	}
	return "[" + inner + "]"
}
