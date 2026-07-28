package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything fn wrote there. Used to assert on human-readable command
// output without wiring cobra's io streams (runCheck writes directly to
// stdout, and the output shape is what's being tested).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan struct{})
	var out bytes.Buffer
	go func() {
		_, _ = io.Copy(&out, r)
		close(done)
	}()
	defer func() {
		os.Stdout = orig
	}()
	fn()
	_ = w.Close()
	<-done
	return out.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunCheck_CleanTree_ExitZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	out := captureStdout(t, func() {
		if err := runCheck(root, false); err != nil {
			t.Errorf("expected nil error for clean tree, got %v", err)
		}
	})
	if !strings.Contains(out, "No problems found") {
		t.Errorf("expected 'No problems found' summary, got %q", out)
	}
}

func TestRunCheck_ErrorFindings_ExitOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusmask"), "*.txt : /[/\n")
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	out := captureStdout(t, func() {
		if err := runCheck(root, false); err != errSilentNonZero {
			t.Errorf("expected errSilentNonZero, got %v", err)
		}
	})
	if !strings.Contains(out, "[error]") {
		t.Errorf("expected an error-tagged finding in output, got %q", out)
	}
}

func TestRunCheck_JSONOutput_ValidSchema(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	// A directory-mask rewrite is a `warn`-severity finding that survives the
	// removal of zero-match reporting, so it exercises the non-error JSON path.
	writeFile(t, filepath.Join(root, ".janusmask"), "secrets\n")
	if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "secrets", "a.txt"), "x")

	out := captureStdout(t, func() {
		_ = runCheck(root, true)
	})
	var report struct {
		Findings []struct {
			Severity string `json:"severity"`
			File     string `json:"file"`
			Message  string `json:"message"`
		} `json:"findings"`
		FileCount int `json:"fileCount"`
		DirCount  int `json:"dirCount"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("JSON output should parse: %v\noutput: %s", err, out)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected at least one finding in JSON output")
	}
	if report.Findings[0].Severity != "warn" {
		t.Errorf("severity should render as string 'warn', got %q", report.Findings[0].Severity)
	}
}

func TestRunCheck_NonexistentRoot_ReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runCheck(filepath.Join(t.TempDir(), "does-not-exist"), false)
	if err == nil {
		t.Fatal("expected error for nonexistent root")
	}
}
