package rules

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTranslateGlobUnit(t *testing.T) {
	cases := []struct {
		line  string
		path  string
		isDir bool
		want  bool
	}{
		{"*.pem", "server.pem", false, true},
		{"*.pem", "dir/server.pem", false, true}, // slash-free pattern matches at any depth (rule 6)
		{"id_rsa*", "id_rsa", false, true},
		{"id_rsa*", "id_rsa.pub", false, true},
		{"secretdir/", "secretdir", true, true},
		{"secretdir/", "secretdir", false, false},
		{"/root.txt", "root.txt", false, true},
		{"/root.txt", "sub/root.txt", false, false},
		{"config/x.yaml", "config/x.yaml", false, true},
		{"config/x.yaml", "sub/config/x.yaml", false, false},
		{"**/x.yaml", "config/x.yaml", false, true},
		{"**/x.yaml", "sub/config/x.yaml", false, true},
		{"**/x.yaml", "x.yaml", false, true},
		{"abc/**", "abc/def", false, true},
		{"abc/**", "abc/def/ghi", false, true},
		{"a/**/b", "a/b", false, true},
		{"a/**/b", "a/x/b", false, true},
		{"a/**/b", "a/x/y/b", false, true},
		{"[ab].txt", "a.txt", false, true},
		{"[ab].txt", "c.txt", false, false},
	}

	for _, c := range cases {
		t.Run(c.line+"_"+c.path, func(t *testing.T) {
			p, err := compilePatternFold(1, c.line, false)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got := p.matches(c.path, c.isDir)
			if got != c.want {
				t.Errorf("compilePattern(%q).matches(%q, isDir=%v) = %v, want %v", c.line, c.path, c.isDir, got, c.want)
			}
		})
	}
}

func TestNegateAndDirOnlyParsing(t *testing.T) {
	p, err := compilePatternFold(1, "!important.log", false)
	if err != nil {
		t.Fatal(err)
	}
	if !p.negate {
		t.Error("expected negate=true")
	}

	p2, err := compilePatternFold(1, `\!literal.log`, false)
	if err != nil {
		t.Fatal(err)
	}
	if p2.negate {
		t.Error("expected escaped '!' to not negate")
	}
	if !p2.matches("!literal.log", false) {
		t.Error("expected escaped pattern to match literal '!' prefix")
	}
}

// TestGitConformance cross-checks translateGlob/compilePattern against real
// `git check-ignore` as an oracle, guarding against semantic drift from git.
// Skips if git is unavailable.
func TestGitConformance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cases := []struct {
		pattern string
		paths   []string
	}{
		{"*.pem", []string{"a.pem", "dir/b.pem", "a.txt"}},
		{"id_rsa*", []string{"id_rsa", "id_rsa.pub", "other"}},
		{"secretdir/", []string{"secretdir", "other/secretdir"}},
		{"/root.txt", []string{"root.txt", "sub/root.txt"}},
		{"config/x.yaml", []string{"config/x.yaml", "sub/config/x.yaml"}},
		{"**/x.yaml", []string{"x.yaml", "config/x.yaml", "sub/config/x.yaml"}},
		{"abc/**", []string{"abc", "abc/def/ghi"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b", "a/c"}},
		{"[ab].txt", []string{"a.txt", "b.txt", "c.txt"}},
	}

	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(c.pattern+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, dir, "init", "-q")

			p, err := compilePatternFold(1, c.pattern, false)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			for _, rel := range c.paths {
				full := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				// Determine dir-ness from the pattern shape: create a real
				// directory when the path looks like a directory target
				// for this specific test (secretdir cases) — otherwise a
				// plain file suffices for git's own matching, since git
				// check-ignore treats non-existent paths as files unless
				// told otherwise. We instead check both interpretations
				// and use whichever exists on disk after creation below.
				isDir := false
				if c.pattern == "secretdir/" && (rel == "secretdir" || rel == "other/secretdir") {
					isDir = true
					if err := os.MkdirAll(full, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if c.pattern == "abc/**" && rel == "abc" {
					isDir = true
					if err := os.MkdirAll(full, 0o755); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				gitIgnored := gitCheckIgnore(t, dir, rel)
				ourMatch := p.matches(filepath.ToSlash(rel), isDir)
				if gitIgnored != ourMatch {
					t.Errorf("pattern %q path %q: git=%v ours=%v", c.pattern, rel, gitIgnored, ourMatch)
				}
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitCheckIgnore(t *testing.T, dir, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-q", rel)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 1 {
			return false
		}
	}
	t.Fatalf("git check-ignore %q: %v", rel, err)
	return false
}
