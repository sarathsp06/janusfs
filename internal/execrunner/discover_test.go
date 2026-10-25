package execrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	if err := os.WriteFile(filepath.Join(root, ".janusfs.yml"), []byte("version: 1\nexec:\n  net: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("walks up to the policy and reads its exec section", func(t *testing.T) {
		plan, err := Discover(nested)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if plan.Src != root {
			t.Errorf("Src = %q, want %q", plan.Src, root)
		}
		if plan.Cwd != nested {
			t.Errorf("Cwd = %q, want %q", plan.Cwd, nested)
		}
		if plan.PolicyPath != filepath.Join(root, ".janusfs.yml") {
			t.Errorf("PolicyPath = %q", plan.PolicyPath)
		}
		if plan.ExecPolicy.Net != "none" {
			t.Errorf("ExecPolicy.Net = %q, want none", plan.ExecPolicy.Net)
		}
	})

	t.Run("refuses with a remedy when no policy exists", func(t *testing.T) {
		_, err := Discover(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "janusfs init") {
			t.Fatalf("err = %v; want a refusal naming `janusfs init`", err)
		}
	})

	t.Run("refuses a policy with an invalid exec section", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".janusfs.yml"), []byte("version: 1\nexec:\n  net: nnoe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(dir)
		if err == nil || !strings.Contains(err.Error(), "exec.net") {
			t.Fatalf("err = %v; want an exec.net refusal", err)
		}
	})
}

func TestInspect(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	files := map[string]string{
		".janusfs.yml": "version: 1\nhide: ['*.pem']\nmask:\n  - paths: ['*.env*']\n    patterns: [env-value]\n",
		"server.pem":   "not really a key",
		".env":         "API_KEY=abc\n",
		"main.go":      "package main\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	ins, err := Inspect(plan, "none", "--net flag")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if ins.NetMode != "none" || ins.NetSource != "--net flag" {
		t.Errorf("net = %q/%q", ins.NetMode, ins.NetSource)
	}
	if len(ins.Hidden) != 1 || ins.Hidden[0].Path != "server.pem" {
		t.Errorf("Hidden = %+v, want server.pem", ins.Hidden)
	}
	if len(ins.Masked) != 1 || ins.Masked[0].Path != ".env" {
		t.Errorf("Masked = %+v, want .env", ins.Masked)
	}
	if ins.FileCount != 4 {
		t.Errorf("FileCount = %d, want 4", ins.FileCount)
	}
	// Enforcement availability is host-dependent; the report must still
	// explain itself either way.
	if !ins.Enforcement.Available && ins.Enforcement.Reason == "" {
		t.Error("unavailable enforcement must carry a reason")
	}
}
