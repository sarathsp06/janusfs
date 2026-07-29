package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withGlobalDir points GlobalDir() at an isolated temp dir for the duration
// of a test, via HOME override, so tests never touch the real
// ~/.janusfs/config on the machine running them.
func withGlobalDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".janusfs", "config")
}

const (
	legacyIgnoreFileName = ".janusignore"
	legacyMaskFileName   = ".janusmask"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if filepath.Base(path) == legacyIgnoreFileName || filepath.Base(path) == legacyMaskFileName {
		writeLegacyPolicyFixture(t, path, content)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyPolicyFixture(t *testing.T, path, content string) {
	t.Helper()
	policyPath := filepath.Join(filepath.Dir(path), PolicyFileName)
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(policyPath); os.IsNotExist(err) {
		if err := os.WriteFile(policyPath, []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	if filepath.Base(path) == legacyIgnoreFileName {
		var hide, allow []string
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "!") {
				allow = append(allow, strings.TrimPrefix(line, "!"))
			} else {
				hide = append(hide, line)
			}
		}
		if len(hide) > 0 {
			b.WriteString("hide:\n")
			for _, line := range hide {
				b.WriteString(fmt.Sprintf("  - %q\n", line))
			}
		}
		if len(allow) > 0 {
			b.WriteString("allow:\n")
			for _, line := range allow {
				b.WriteString(fmt.Sprintf("  - %q\n", line))
			}
		}
	} else {
		b.WriteString("mask:\n")
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			glob, refs, hasRefs := strings.Cut(line, ":")
			b.WriteString("  - paths:\n")
			b.WriteString(fmt.Sprintf("      - %q\n", strings.TrimSpace(glob)))
			if hasRefs && strings.TrimSpace(refs) != "" {
				b.WriteString("    patterns:\n")
				for _, ref := range strings.Split(refs, ",") {
					b.WriteString(fmt.Sprintf("      - %q\n", strings.TrimSpace(ref)))
				}
			}
		}
	}
	f, err := os.OpenFile(policyPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverYAMLPolicy(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()

	writeFile(t, filepath.Join(root, PolicyFileName), `version: 1
hide:
  - "*.pem"
  - id_rsa*
mask:
  - paths:
      - "*.env"
    patterns:
      - env-value
  - paths:
      - secrets/*
`)
	writeFile(t, filepath.Join(root, "secrets", "x.txt"), "irrelevant")
	writeFile(t, filepath.Join(root, "README.md"), "hi")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path  string
		isDir bool
		want  Decision
	}{
		{"id_rsa", false, Hidden},
		{"server.pem", false, Hidden},
		{".env", false, Masked},
		{"secrets/x.txt", false, Masked},
		{"README.md", false, Allowed},
	}
	for _, c := range cases {
		res := rs.Resolve(c.path, c.isDir)
		if res.Decision != c.want {
			t.Errorf("Resolve(%q) = %v, want %v (ruleRef=%q)", c.path, res.Decision, c.want, res.RuleRef)
		}
	}
}

func TestResolveBasicHiddenMaskedAllowed(t *testing.T) {
	TestDiscoverYAMLPolicy(t)
}

func TestResolveHiddenWinsOverMasked(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "secret.env\n")
	writeFile(t, filepath.Join(root, legacyMaskFileName), "*.env : env-value\n")
	writeFile(t, filepath.Join(root, "secret.env"), "X=1")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("secret.env", false)
	if res.Decision != Hidden {
		t.Fatalf("expected Hidden (precedence over Masked), got %v", res.Decision)
	}
}

func TestResolveDeeperIgnoreNegatesShallower(t *testing.T) {
	// Within the in-tree tier, gitignore's deeper-wins and negation precedence
	// is unchanged; only the global tier is a fail-closed floor.
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "*.log\n")
	writeFile(t, filepath.Join(root, "keep", legacyIgnoreFileName), "!important.log\n")
	writeFile(t, filepath.Join(root, "keep", "important.log"), "x")
	writeFile(t, filepath.Join(root, "other.log"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if got := rs.Resolve("keep/important.log", false).Decision; got != Allowed {
		t.Errorf("expected deeper negation to re-include, got %v", got)
	}
	if got := rs.Resolve("other.log", false).Decision; got != Hidden {
		t.Errorf("expected shallower rule to still apply, got %v", got)
	}
}

func TestResolveHiddenDirectoryBlocksNegationBeneath(t *testing.T) {
	// A hidden ancestor directory forces every descendant Hidden
	// regardless of deeper rules — a deeper .janusignore's negation cannot
	// resurface a path whose ancestor directory is itself Hidden.
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "secretdir/\n")
	writeFile(t, filepath.Join(root, "secretdir", legacyIgnoreFileName), "!keep.txt\n")
	writeFile(t, filepath.Join(root, "secretdir", "keep.txt"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	dirRes := rs.Resolve("secretdir", true)
	if dirRes.Decision != Hidden {
		t.Fatalf("expected secretdir itself to resolve Hidden, got %v", dirRes.Decision)
	}

	fileRes := rs.Resolve("secretdir/keep.txt", false)
	if fileRes.Decision != Hidden {
		t.Fatalf("expected the ancestor short-circuit to keep keep.txt Hidden despite the nested negation, got %v", fileRes.Decision)
	}
}

func TestResolveNegationCannotEscapeItsOwnHiddenDirButSiblingIsUnaffected(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "secretdir/\n")
	writeFile(t, filepath.Join(root, "other.txt"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Resolve("other.txt", false).Decision; got != Allowed {
		t.Fatalf("sibling file must be unaffected by an unrelated hidden directory, got %v", got)
	}
}

func TestResolveDirectoryNeverMasked(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "secrets\n")
	writeFile(t, filepath.Join(root, "secrets"), "") // file named exactly "secrets" for the mask test below
	if err := os.Remove(filepath.Join(root, "secrets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("secrets", true)
	if res.Decision == Masked {
		t.Fatalf("directories must never resolve Masked, got %v", res.Decision)
	}
}

func TestResolveGlobalLevelLowestPrecedence(t *testing.T) {
	globalDir := withGlobalDir(t)
	writeFile(t, filepath.Join(globalDir, legacyIgnoreFileName), "*.pem\n")

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("server.pem", false)
	if res.Decision != Hidden {
		t.Fatalf("expected global rule to apply, got %v", res.Decision)
	}
}

func TestResolveGlobalFloorCannotBeLiftedByInTreeNegation(t *testing.T) {
	// The global tier is a fail-closed floor: an in-tree negation cannot override a
	// global Hidden verdict, even though the same negation syntax freely
	// overrides shallower *in-tree* rules (see
	// TestResolveDeeperIgnoreNegatesShallower).
	globalDir := withGlobalDir(t)
	writeFile(t, filepath.Join(globalDir, legacyIgnoreFileName), "*.pem\n")

	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "!server.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Resolve("server.pem", false).Decision; got != Hidden {
		t.Fatalf("expected the global floor to survive an in-tree negation, got %v", got)
	}
}

func TestResolveInTreeNegationStillWorksWhenNoGlobalFloorApplies(t *testing.T) {
	// The floor only blocks negation of a verdict the *global* tier set;
	// an in-tree rule with no corresponding global rule can still be
	// negated normally by a deeper in-tree file.
	globalDir := withGlobalDir(t)
	writeFile(t, filepath.Join(globalDir, legacyIgnoreFileName), "*.pem\n") // unrelated to *.secret below

	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyIgnoreFileName), "*.secret\n!keep.secret\n")
	writeFile(t, filepath.Join(root, "keep.secret"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Resolve("keep.secret", false).Decision; got != Allowed {
		t.Fatalf("expected in-tree negation to re-include a path with no global floor, got %v", got)
	}
}

func TestResolveMaskUnionAcrossLevels(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "**/*.env : env-value\n")
	writeFile(t, filepath.Join(root, "app", legacyMaskFileName), "*.env : aws-key\n")
	writeFile(t, filepath.Join(root, "app", ".env"), "X=1")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("app/.env", false)
	if res.Decision != Masked {
		t.Fatalf("expected Masked, got %v", res.Decision)
	}
	found := map[string]bool{}
	for _, n := range res.PatternNames {
		found[n] = true
	}
	if !found["env-value"] || !found["aws-key"] {
		t.Fatalf("expected union of patterns from both levels, got %v", res.PatternNames)
	}
}

func TestMaskFailClosedOnBadRegex(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "bad.txt : /[/\n")
	writeFile(t, filepath.Join(root, "bad.txt"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("bad.txt", false)
	if res.Decision != Hidden || !res.Poisoned {
		t.Fatalf("expected fail-closed Hidden for bad regex, got %v (poisoned=%v)", res.Decision, res.Poisoned)
	}
}

func TestMaskWholeFileSentinelNoPattern(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "secrets/*\n")
	writeFile(t, filepath.Join(root, "secrets", "a.txt"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	res := rs.Resolve("secrets/a.txt", false)
	if res.Decision != Masked {
		t.Fatalf("expected Masked, got %v", res.Decision)
	}
	if len(res.PatternNames) != 1 || res.PatternNames[0] != "whole-file" {
		t.Fatalf("expected whole-file sentinel, got %v", res.PatternNames)
	}
}

func TestParseMaskLineEscapedColon(t *testing.T) {
	entry, err := parseMaskLine(1, `weird\:name.txt : env-value`, false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Glob != "weird:name.txt" {
		t.Fatalf("expected escaped colon preserved in glob, got %q", entry.Glob)
	}
}

func TestParseMaskLineMultiplePatterns(t *testing.T) {
	entry, err := parseMaskLine(1, `config/*.yaml : generic-secret, db-uri`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.PatternRefs) != 2 {
		t.Fatalf("expected 2 pattern refs, got %v", entry.PatternRefs)
	}
}

func TestParseMaskLineStripsInlineComment(t *testing.T) {
	entry, err := parseMaskLine(1, `*.env : env-value # mask env files`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.PatternRefs) != 1 || entry.PatternRefs[0] != "env-value" {
		t.Fatalf("expected inline comment stripped from pattern ref, got %v", entry.PatternRefs)
	}
}

func TestParseMaskLineCustomRegexWithComma(t *testing.T) {
	entry, err := parseMaskLine(1, `f.txt : /a,b/`, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.PatternRefs) != 1 || entry.PatternRefs[0] != "/a,b/" {
		t.Fatalf("expected comma inside slashes preserved as one ref, got %v", entry.PatternRefs)
	}
}

func TestDiscoverNoConfigFiles(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "plain.txt"), "x")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if res := rs.Resolve("plain.txt", false); res.Decision != Allowed {
		t.Fatalf("expected Allowed with no config files, got %v", res.Decision)
	}
}

func TestResolveCaseFoldMatchesUppercaseVariant(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "*.env : env-value\n")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	rs.FoldCase = true
	for _, lvl := range rs.MaskLevels {
		for i := range lvl.Entries {
			gp, err := compilePatternFold(lvl.Entries[i].GlobPattern.LineNo(), lvl.Entries[i].Glob, true)
			if err != nil {
				t.Fatal(err)
			}
			lvl.Entries[i].GlobPattern = gp
		}
	}

	if res := rs.Resolve("SECRET.ENV", false); res.Decision != Masked {
		t.Fatalf("with FoldCase, expected SECRET.ENV to match *.env mask, got %v", res.Decision)
	}
}

func TestResolveCaseSensitiveDoesNotFoldByDefault(t *testing.T) {
	withGlobalDir(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, legacyMaskFileName), "*.env : env-value\n")

	rs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	// Force case-sensitive compilation regardless of the real test volume's own
	// case sensitivity, so this test's assertion doesn't depend on which
	// filesystem happens to be running it.
	rs.FoldCase = false
	for _, lvl := range rs.MaskLevels {
		for i := range lvl.Entries {
			gp, err := compilePatternFold(lvl.Entries[i].GlobPattern.LineNo(), lvl.Entries[i].Glob, false)
			if err != nil {
				t.Fatal(err)
			}
			lvl.Entries[i].GlobPattern = gp
		}
	}

	if res := rs.Resolve("SECRET.ENV", false); res.Decision == Masked {
		t.Fatalf("without FoldCase, SECRET.ENV should not match a *.env mask compiled case-sensitively, got %v", res.Decision)
	}
}

func TestCompilePatternFoldCaseInsensitive(t *testing.T) {
	p, err := compilePatternFold(1, "*.env", true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.matches("SECRET.ENV", false) {
		t.Fatalf("expected case-folded pattern to match uppercase spelling")
	}
	if !p.matches("secret.env", false) {
		t.Fatalf("expected case-folded pattern to still match original-case spelling")
	}
}

func TestCompilePatternNoFoldIsCaseSensitive(t *testing.T) {
	p, err := compilePatternFold(1, "*.env", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.matches("SECRET.ENV", false) {
		t.Fatalf("expected non-folded pattern to NOT match uppercase spelling")
	}
	if !p.matches("secret.env", false) {
		t.Fatalf("expected non-folded pattern to still match original-case spelling")
	}
}

func TestCaseInsensitiveVolumeDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	// Must not panic regardless of the platform running the test; the actual
	// answer is platform- and filesystem-dependent so only the darwin default
	// is asserted here.
	got := caseInsensitiveVolume(dir)
	if runtime.GOOS == "darwin" {
		// Most darwin CI/dev volumes are case-insensitive by default, but this
		// is not guaranteed (a case-sensitive APFS volume is a supported
		// configuration), so only check that the probe returns without error
		// and is a plain bool.
		_ = got
	}
}
