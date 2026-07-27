package check

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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

func TestCheckZeroMatchGlob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.doesnotexist\n")
	writeFile(t, filepath.Join(root, "README.md"), "x")

	r, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(findMessages(r), "matches no files") {
		t.Fatalf("expected a zero-match finding, got %v", findMessages(r))
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
