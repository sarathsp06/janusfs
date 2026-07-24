// Package patterns implements the built-in redaction pattern library
// (SPEC.md FR-16) and the custom-regex wrapper used by .janusmask (FR-13).
//
// This package only compiles and describes patterns; it does not find spans
// or redact bytes (that's internal/redact, Phase 2) and it is not wired into
// the mount yet. It exists now because .janusmask lines reference pattern
// names, and janusfs check/explain (docs/SPEC_AMENDMENTS.md 2026-07-17) must
// be able to validate those names and report what they mean without waiting
// for the full masking pipeline.
package patterns

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Pattern describes one compiled redaction pattern: a name (builtin or a
// literal custom regex source), the compiled matcher, and which capture
// group holds the maskable span (FR-14: group 1 if the regex defines one,
// else the whole match — group index 0).
type Pattern struct {
	// Name is the builtin's reserved name, or the literal /regex/ source for
	// a custom pattern (used in reporting; not a stable identity).
	Name string

	// Builtin is true for one of the FR-16 reserved names.
	Builtin bool

	// Regex is nil only for the "whole-file" sentinel, which masks every
	// byte of a file without any matching (SPEC §8.1).
	Regex *regexp.Regexp

	// GroupIndex is the capture group whose span is masked (FR-14): 0 means
	// "the whole match" (no capture group / regex has no groups).
	GroupIndex int

	// WholeFile is the FR-16 sentinel: mask every byte of the file, no
	// pattern matching involved.
	WholeFile bool

	// PreFilter returns true if the pattern might match buf, or false if it definitely cannot.
	PreFilter func(buf []byte) bool
}

// WholeFileName is the FR-16 sentinel pattern name.
const WholeFileName = "whole-file"

// builtinSpec is the declarative source for one row of FR-16's table before
// compilation (letting registerBuiltins stay a short, obviously-correct
// loop rather than repeating regexp.MustCompile+field-assignment per entry).
type builtinSpec struct {
	name       string
	expr       string // empty for whole-file
	groupIndex int
}

// builtinSpecs is FR-16's table, verbatim. Order matches the spec for easy
// side-by-side review. Changing any expr here is a breaking change to the
// pattern library and must bump patternsVersion (reported by `doctor`/UI
// once those exist — SPEC FR-16's "patterns_version" requirement).
var builtinSpecs = []builtinSpec{
	{name: "env-value", expr: `(?m)^\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.+?)\s*$`, groupIndex: 1},
	{name: "aws-key", expr: `\b((?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16})\b`, groupIndex: 1},
	{name: "private-key", expr: `(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`, groupIndex: 0},
	{name: "jwt", expr: `\b(eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b`, groupIndex: 1},
	{name: "db-uri", expr: `\b[a-z][a-z0-9+.-]*://([^\s:@/]+:[^\s@/]+)@`, groupIndex: 1},
	{name: "github-token", expr: `\b((?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,255})\b`, groupIndex: 1},
	{name: "generic-secret", expr: `(?im)\b(?:password|passwd|secret|token|api[_-]?key)\b\s*[:=]\s*["']?([^\s"']{6,})`, groupIndex: 1},
}

// awsSecretKeySpec is aws-key's second regex (FR-16 lists two regexes under
// one name); modeled as a second builtin entry sharing the "aws-key" name so
// both are tried (union of matches, same as any two patterns on one glob).
var awsSecretKeySpec = builtinSpec{
	name:       "aws-key",
	expr:       `(?i)\baws_secret_access_key\b\s*[=:]\s*([A-Za-z0-9/+=]{40})`,
	groupIndex: 1,
}

// builtins is the compiled registry, keyed by name. aws-key maps to a slice
// of two compiled patterns internally via builtinVariants; Lookup exposes
// them as a single logical Pattern list so callers don't need to know a name
// can expand to more than one regex.
var builtinVariants = map[string][]*Pattern{}

// ReservedNames is the FR-16 "names reserved; user regex may not shadow a
// builtin name" set, including the whole-file sentinel.
var ReservedNames = map[string]bool{}

func init() {
	register := func(spec builtinSpec) {
		p := &Pattern{Name: spec.name, Builtin: true, GroupIndex: spec.groupIndex}
		if spec.expr != "" {
			p.Regex = regexp.MustCompile(spec.expr)
		}
		p.PreFilter = getBuiltinPreFilter(spec.name, spec.expr)
		builtinVariants[spec.name] = append(builtinVariants[spec.name], p)
		ReservedNames[spec.name] = true
	}
	for _, s := range builtinSpecs {
		register(s)
	}
	register(awsSecretKeySpec)
	ReservedNames[WholeFileName] = true
}

func containsIgnoreCase(buf []byte, lowerStr string) bool {
	if len(lowerStr) == 0 {
		return true
	}
	if len(buf) < len(lowerStr) {
		return false
	}
	for i := 0; i <= len(buf)-len(lowerStr); i++ {
		match := true
		for j := 0; j < len(lowerStr); j++ {
			b := buf[i+j]
			if b >= 'A' && b <= 'Z' {
				b = b + 32
			}
			if b != lowerStr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func getBuiltinPreFilter(name, expr string) func(buf []byte) bool {
	switch name {
	case "env-value":
		return func(buf []byte) bool {
			return bytes.Contains(buf, []byte{'='})
		}
	case "aws-key":
		if strings.Contains(expr, "AKIA") {
			return func(buf []byte) bool {
				return bytes.Contains(buf, []byte("AKIA")) ||
					bytes.Contains(buf, []byte("ASIA")) ||
					bytes.Contains(buf, []byte("ABIA")) ||
					bytes.Contains(buf, []byte("ACCA"))
			}
		}
		return func(buf []byte) bool {
			return containsIgnoreCase(buf, "aws_secret_access_key")
		}
	case "private-key":
		return func(buf []byte) bool {
			return bytes.Contains(buf, []byte("-----BEGIN"))
		}
	case "jwt":
		return func(buf []byte) bool {
			return bytes.Contains(buf, []byte("eyJ"))
		}
	case "db-uri":
		return func(buf []byte) bool {
			return bytes.Contains(buf, []byte("://"))
		}
	case "github-token":
		return func(buf []byte) bool {
			return bytes.Contains(buf, []byte("ghp_")) ||
				bytes.Contains(buf, []byte("gho_")) ||
				bytes.Contains(buf, []byte("ghu_")) ||
				bytes.Contains(buf, []byte("ghs_")) ||
				bytes.Contains(buf, []byte("ghr_"))
		}
	case "generic-secret":
		return func(buf []byte) bool {
			if !bytes.Contains(buf, []byte{':'}) && !bytes.Contains(buf, []byte{'='}) {
				return false
			}
			return containsIgnoreCase(buf, "password") ||
				containsIgnoreCase(buf, "passwd") ||
				containsIgnoreCase(buf, "secret") ||
				containsIgnoreCase(buf, "token") ||
				containsIgnoreCase(buf, "api-key") ||
				containsIgnoreCase(buf, "api_key") ||
				containsIgnoreCase(buf, "apikey")
		}
	default:
		return nil
	}
}

// LookupBuiltin returns the compiled Pattern(s) for a reserved builtin name.
// Most names resolve to exactly one Pattern; "aws-key" resolves to two (FR-16
// lists two regexes under that name — matches are unioned per FR-14). The
// "whole-file" sentinel resolves to a single Pattern with WholeFile set and
// no Regex.
func LookupBuiltin(name string) ([]*Pattern, bool) {
	if name == WholeFileName {
		return []*Pattern{{Name: WholeFileName, Builtin: true, WholeFile: true}}, true
	}
	ps, ok := builtinVariants[name]
	return ps, ok
}

// IsReserved reports whether name is a builtin or the whole-file sentinel,
// per FR-16's "user regex may not shadow a builtin name."
func IsReserved(name string) bool {
	return ReservedNames[name]
}

// ParsePatternRef parses one comma-separated pattern reference from a
// .janusmask line (FR-13's grammar: `<builtin-name> | /<RE2-regex>/`) into
// either a builtin lookup or a compiled custom Pattern.
//
// A custom pattern's Name is set to its literal source ("/regex/", without
// the slashes) for reporting; ParsePatternRef itself does not enforce the
// reserved-name rule for custom patterns because that rule only applies to
// a *name* a user might invent to shadow a builtin — a raw /regex/ has no
// name to shadow with. (Reserved-name shadowing is rejected earlier, when a
// bare non-slashed token is looked up and happens to coincide with a
// builtin's exact spelling but the caller intended something else — that
// case doesn't arise here since bare tokens always mean "use the builtin.")
func ParsePatternRef(ref string) ([]*Pattern, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("patterns: empty pattern reference")
	}

	if strings.HasPrefix(ref, "/") {
		if len(ref) < 2 || !strings.HasSuffix(ref, "/") {
			return nil, fmt.Errorf("patterns: custom regex %q missing closing '/'", ref)
		}
		expr := ref[1 : len(ref)-1]
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("patterns: compiling custom regex %q: %w", expr, err)
		}
		group := 0
		if re.NumSubexp() >= 1 {
			group = 1
		}
		return []*Pattern{{Name: ref, Builtin: false, Regex: re, GroupIndex: group}}, nil
	}

	ps, ok := LookupBuiltin(ref)
	if !ok {
		return nil, fmt.Errorf("patterns: unknown builtin pattern %q", ref)
	}
	return ps, nil
}
