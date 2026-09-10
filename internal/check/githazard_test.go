package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestGitStagingHazards(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".janusfs.yml", "version: 1\nmask:\n  - paths: [\"*.env\"]\n    patterns: [aws-key]\n")
	write("app.env", "AKIA1234567890ABCDEF") // masked, untracked, not ignored -> hazard
	write("README.md", "hello")              // allowed -> not a hazard
	write("ignored.env", "AKIA")             // masked but gitignored -> not a hazard
	write(".gitignore", "ignored.env\n")

	hazards, err := GitStagingHazards(root)
	if err != nil {
		t.Fatalf("GitStagingHazards: %v", err)
	}
	got := map[string]bool{}
	for _, h := range hazards {
		got[h] = true
	}
	if !got["app.env"] {
		t.Errorf("expected app.env reported as hazard, got %v", hazards)
	}
	if got["README.md"] {
		t.Errorf("allowed file README.md must not be a hazard, got %v", hazards)
	}
	if got["ignored.env"] {
		t.Errorf("gitignored ignored.env must not be a hazard, got %v", hazards)
	}
}

func TestGitStagingHazardsNoRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".janusfs.yml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hazards, err := GitStagingHazards(root)
	if err != nil || hazards != nil {
		t.Fatalf("non-git dir: want (nil, nil), got (%v, %v)", hazards, err)
	}
}
