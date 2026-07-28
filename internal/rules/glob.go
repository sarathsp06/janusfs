package rules

import (
	"fmt"
	"regexp"
	"strings"
)

// ignorePattern is one compiled line of a .janusignore file, or one glob
// half of a .janusmask line. Implemented from scratch rather
// than via a third-party gitignore library. The previously-vendored
// github.com/sabhiram/go-gitignore is low-activity and its per-file negation
// model doesn't compose across hierarchical levels: a deeper file's negation
// must be able to re-include a path a shallower file excluded, and that library
// evaluates each file in isolation. Precedence across levels is handled by
// evaluating one ordered, cross-level pattern list (see resolve.go), so
// this type only needs to compile and match a single line correctly.
//
// Replacing this matcher with bmatcuk/doublestar plus a no-negation model was
// considered and withdrawn: gitignore's "no-slash pattern matches at every depth"
// rule is the fail-closed default a secret-hiding tool wants, and a
// per-level doublestar match would have silently under-matched nested
// files instead. That amendment kept this matcher and gitignore semantics
// exactly as documented below, and added one precedence change on top (see
// resolve.go's global-tier floor) rather than replacing the matcher.
//
// Semantics are cross-checked against real `git check-ignore`; see glob_test.go's
// conformance cases.
type ignorePattern struct {
	negate  bool
	dirOnly bool
	re      *regexp.Regexp
	lineNo  int
	raw     string // original line, unmodified, for reporting
}

// compilePatternFold parses and compiles one non-blank, non-comment line — a
// .janusignore line or a .janusmask glob — into an ignorePattern, with foldCase
// controlling whether the compiled regex matches case-insensitively. This must agree with the backing
// filesystem: on a case-insensitive volume (APFS/HFS+ default), the kernel
// resolves "SECRET.ENV" and "secret.env" to the same inode, so a
// case-sensitive glob would let ".ENV" slip past a "*.env" mask rule.
func compilePatternFold(lineNo int, raw string, foldCase bool) (*ignorePattern, error) {
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
		// A pattern with a slash anywhere but a trailing one is
		// relative only to its own directory, not re-checked at every
		// depth (verified against `git check-ignore`: "config/x.yaml" in a
		// root .gitignore does NOT match "sub/config/x.yaml").
		full = "^" + core + "$"
	} else {
		// A slash-free pattern matches its target at any depth under the
		// pattern's own directory.
		full = "^(?:.*/)?" + core + "$"
	}
	if foldCase {
		full = "(?i)" + full
	}

	re, err := regexp.Compile(full)
	if err != nil {
		return nil, fmt.Errorf("rules: line %d: compiling %q: %w", lineNo, raw, err)
	}
	return &ignorePattern{negate: negate, dirOnly: dirOnly, re: re, lineNo: lineNo, raw: raw}, nil
}

// matches reports whether relPath (slash-separated, relative to this
// pattern's own directory, no leading/trailing slash) is targeted by p.
// dirOnly patterns only ever match a directory being tested as itself. They are
// not expected to independently express "and everything beneath it" — that is
// the ancestor short-circuit's job, in Resolve.
func (p *ignorePattern) matches(relPath string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return p.re.MatchString(relPath)
}

// Matches is matches's exported form, for consumers outside this package
// that hold a pattern value via IgnoreLevel.Patterns or MaskEntry.GlobPattern
// — internal/check's static lint — without needing to name the unexported
// ignorePattern type themselves.
func (p *ignorePattern) Matches(relPath string, isDir bool) bool { return p.matches(relPath, isDir) }

// LineNo returns the 1-based source line this pattern was compiled from.
func (p *ignorePattern) LineNo() int { return p.lineNo }

// Negate reports whether this is a "!"-negation pattern.
func (p *ignorePattern) Negate() bool { return p.negate }

// Raw returns the original, unmodified source line.
func (p *ignorePattern) Raw() string { return p.raw }

// translateGlob compiles one anchor-stripped, dir-marker-stripped gitignore
// glob into a regex fragment matching a single relative path (using '/' as
// the component separator). It supports '*', '?', '[...]' character
// classes, and the four '**' forms: bare "**", leading
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
