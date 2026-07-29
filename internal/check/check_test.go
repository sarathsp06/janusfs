package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	switch filepath.Base(path) {
	case ".janusignore", ".janusmask":
		appendPolicyFixture(t, path, content)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendPolicyFixture(t *testing.T, path, content string) {
	t.Helper()
	policyPath := filepath.Join(filepath.Dir(path), ".janusfs.yml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(policyPath); os.IsNotExist(err) {
		if err := os.WriteFile(policyPath, []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	if filepath.Base(path) == ".janusignore" {
		var hide, allow []string
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
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
			if line == "" {
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

func findMessages(r Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Message)
	}
	return out
}

func containsSubstr(list []string, sub string) bool {
	for _, s := range list {
		if len(sub) <= len(s) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// TestCheckZeroMatchIsNotAFinding locks in that a rule matching no file today
// is NOT reported: a defensive `.janusignore`/`.janusmask` pattern covering
// files that don't exist yet (id_rsa*, *.pem, …) is intended, not a mistake,
// and warning about it only trains users to ignore check output.
func TestCheckZeroMatchIsNotAFinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.doesnotexist\nid_rsa*\n")
	writeFile(t, filepath.Join(root, ".janusmask"), "*.nope : env-value\n")
	writeFile(t, filepath.Join(root, "README.md"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if containsSubstr(findMessages(r), "matches no files") {
		t.Fatalf("zero-match patterns are defensive and must not be reported, got %v", findMessages(r))
	}
	if len(r.Findings) != 0 {
		t.Fatalf("expected no findings for a tree of only defensive non-matching rules, got %v", findMessages(r))
	}
}

func TestCheckDirectoryMaskRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusmask"), "secrets\n")
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "which can never be Masked") {
		t.Fatalf("expected directory-mask message, got %v", findMessages(r))
	}
}

func TestCheckUnknownBuiltin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusmask"), "*.txt : not-a-real-builtin\n")
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasErrors() {
		t.Fatalf("expected an error for unknown builtin, got %v", findMessages(r))
	}
}

func TestCheckBadRegex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusmask"), "*.txt : /[/\n")
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasErrors() {
		t.Fatalf("expected an error for bad regex, got %v", findMessages(r))
	}
}

func TestCheckHiddenDirNegationWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "secretdir/\n")
	writeFile(t, filepath.Join(root, "secretdir", ".janusignore"), "!keep.txt\n")
	writeFile(t, filepath.Join(root, "secretdir", "keep.txt"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "has no effect") {
		t.Fatalf("expected a hidden-dir negation warning, got %v", findMessages(r))
	}
}

func TestCheckGlobalFloorNegationWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".janusfs", "config", ".janusignore"), "*.pem\n")

	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "!server.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "global rule floor") {
		t.Fatalf("expected a global-floor negation warning, got %v", findMessages(r))
	}
}

func TestCheckRedundantDuplicateLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "sub", ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "sub", "x.pem"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "redundant") {
		t.Fatalf("expected a redundancy finding, got %v", findMessages(r))
	}
}

func TestCheckSecretsReportsAllowedSecretFilename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=super-secret-value\n")

	r, err := RunWithOptions(root, Options{Secrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "likely secret file .env is currently Allowed") {
		t.Fatalf("expected allowed .env warning, got %v", findMessages(r))
	}
}

func TestCheckSecretsDoesNotReportHiddenSecretFilename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), ".env\n")
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=super-secret-value\n")

	r, err := RunWithOptions(root, Options{Secrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if containsSubstr(findMessages(r), "likely secret file .env") {
		t.Fatalf("expected hidden .env not to be reported, got %v", findMessages(r))
	}
}

func TestCheckSecretsReportsAllowedSecretContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "config.txt"), "password = super-secret-value\n")

	r, err := RunWithOptions(root, Options{Secrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "likely secret content in config.txt is currently Allowed") {
		t.Fatalf("expected allowed secret-content warning, got %v", findMessages(r))
	}
}

func TestCheckDoesNotScanSecretsByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=super-secret-value\n")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 0 {
		t.Fatalf("expected default check not to run heuristic secret scan, got %v", findMessages(r))
	}
}

func TestCheckCleanTreeNoFindings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, "README.md"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasErrors() {
		t.Fatalf("expected no errors for a clean tree, got %v", findMessages(r))
	}
	if r.FileCount == 0 {
		t.Fatal("expected non-zero file count")
	}
}

func TestCheckMatchesReportsHiddenAndMaskedOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusfs.yml"), `version: 1
hide:
  - "*.pem"
mask:
  - paths:
      - "*.env"
    patterns:
      - env-value
`)
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=secret\n")
	writeFile(t, filepath.Join(root, "README.md"), "hello")

	r, err := RunWithOptions(root, Options{Matches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 2 {
		t.Fatalf("expected hidden+masked matches only, got %#v", r.Matches)
	}
	got := map[string]string{}
	for _, m := range r.Matches {
		got[m.Path] = m.Decision
	}
	if got["server.pem"] != "HIDDEN" {
		t.Fatalf("server.pem decision = %q, want HIDDEN; matches=%#v", got["server.pem"], r.Matches)
	}
	if got[".env"] != "MASKED" {
		t.Fatalf(".env decision = %q, want MASKED; matches=%#v", got[".env"], r.Matches)
	}
	if _, ok := got["README.md"]; ok {
		t.Fatalf("ALLOWED README.md should not be included by --matches: %#v", r.Matches)
	}
}
