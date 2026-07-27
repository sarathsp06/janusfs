package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExplain_HiddenPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	out := captureStdout(t, func() {
		if err := runExplain(root, filepath.Join(root, "server.pem"), false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "HIDDEN") {
		t.Errorf("expected HIDDEN in output, got %q", out)
	}
	if !strings.Contains(out, "*.pem") {
		t.Errorf("expected the deciding rule text in trace, got %q", out)
	}
}

func TestRunExplain_MaskedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusmask"), "*.env : env-value\n")
	writeFile(t, filepath.Join(root, ".env"), "API=x")

	out := captureStdout(t, func() {
		if err := runExplain(root, filepath.Join(root, ".env"), false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "MASKED") {
		t.Errorf("expected MASKED in output, got %q", out)
	}
	if !strings.Contains(out, "env-value") {
		t.Errorf("expected pattern name in output, got %q", out)
	}
}

func TestRunExplain_AllowedPath_NoRuleMatched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "x")

	out := captureStdout(t, func() {
		if err := runExplain(root, filepath.Join(root, "README.md"), false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "ALLOWED") {
		t.Errorf("expected ALLOWED in output, got %q", out)
	}
	if !strings.Contains(out, "no rule matched") {
		t.Errorf("expected 'no rule matched' explanation, got %q", out)
	}
}

func TestRunExplain_JSONShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "x.pem"), "x")

	out := captureStdout(t, func() {
		if err := runExplain(root, filepath.Join(root, "x.pem"), true); err != nil {
			t.Fatal(err)
		}
	})
	var got explainResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("JSON output should parse: %v\noutput: %s", err, out)
	}
	if got.Decision != "HIDDEN" {
		t.Errorf("decision = %q, want HIDDEN", got.Decision)
	}
	if got.Path != "x.pem" {
		t.Errorf("path = %q, want relative x.pem", got.Path)
	}
}

func TestRunExplain_RelativeTargetResolvesUnderRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "sub", "x.pem"), "x")

	out := captureStdout(t, func() {
		// relative target ("sub/x.pem" from CWD, not from root) — we
		// exercise the "join under root" path by passing a bare relative
		// name.
		if err := runExplain(root, "sub/x.pem", false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "HIDDEN") {
		t.Errorf("expected HIDDEN, got %q", out)
	}
}

func TestRunExplain_TargetOutsideRoot_ReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	// A sibling directory can't be under root, and we don't want to
	// silently reinterpret it as "under root" — that would be a footgun
	// for users trying to debug decisions.
	sibling := t.TempDir()
	writeFile(t, filepath.Join(sibling, "outside.txt"), "x")

	if err := runExplain(root, filepath.Join(sibling, "outside.txt"), false); err == nil {
		t.Fatal("expected error for target outside root")
	}
}

func TestRunExplain_NonexistentRoot_ReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runExplain(filepath.Join(t.TempDir(), "nope"), "anything", false); err == nil {
		t.Fatal("expected error for nonexistent root")
	}
}

func TestRunExplain_RootIsFileNotDir_ReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	filePath := filepath.Join(root, "not-a-dir.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runExplain(filePath, "whatever", false); err == nil {
		t.Fatal("expected error when root is a file, not a directory")
	}
}

func TestRunExplain_NonexistentTarget_StillExplainable(t *testing.T) {
	// A decision doesn't depend on the target existing, so explain
	// should work against a hypothetical path too.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")

	out := captureStdout(t, func() {
		if err := runExplain(root, "does-not-exist.pem", false); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "HIDDEN") {
		t.Errorf("expected HIDDEN for a hypothetical *.pem path, got %q", out)
	}
}
