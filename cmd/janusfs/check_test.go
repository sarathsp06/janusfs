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
		b.WriteString("hide:\n")
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				b.WriteString("  - \"" + line + "\"\n")
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
			b.WriteString("      - \"" + strings.TrimSpace(glob) + "\"\n")
			if hasRefs && strings.TrimSpace(refs) != "" {
				b.WriteString("    patterns:\n")
				for _, ref := range strings.Split(refs, ",") {
					b.WriteString("      - \"" + strings.TrimSpace(ref) + "\"\n")
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

func TestRunCheck_CleanTree_ExitZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	out := captureStdout(t, func() {
		if err := runCheck(root, false, false, false); err != nil {
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
		if err := runCheck(root, false, false, false); err != errSilentNonZero {
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
		_ = runCheck(root, true, false, false)
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

func TestRunCheck_SecretsFlagReportsAllowedSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=super-secret-value\n")

	out := captureStdout(t, func() {
		if err := runCheck(root, false, true, false); err != nil {
			t.Errorf("expected nil error for heuristic secret warnings, got %v", err)
		}
	})
	if !strings.Contains(out, "likely secret file .env is currently Allowed") {
		t.Errorf("expected --secrets warning in output, got %q", out)
	}
}

func TestRunCheck_SecretsJSONOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=super-secret-value\n")

	out := captureStdout(t, func() {
		if err := runCheck(root, true, true, false); err != nil {
			t.Errorf("expected nil error for heuristic secret warnings, got %v", err)
		}
	})
	var report struct {
		Findings []struct {
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("JSON output should parse: %v\noutput: %s", err, out)
	}
	if len(report.Findings) != 1 || report.Findings[0].Severity != "warn" || !strings.Contains(report.Findings[0].Message, "likely secret file .env") {
		t.Fatalf("expected one warn finding for .env, got %+v", report.Findings)
	}
}

func TestRunCheck_NonexistentRoot_ReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runCheck(filepath.Join(t.TempDir(), "does-not-exist"), false, false, false)
	if err == nil {
		t.Fatal("expected error for nonexistent root")
	}
}

func TestRunCheckMatchesHumanOutput(t *testing.T) {
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

	out := captureStdout(t, func() {
		if err := runCheck(root, false, false, true); err != nil {
			t.Fatalf("runCheck returned error: %v", err)
		}
	})
	if !strings.Contains(out, "Policy matches:") {
		t.Fatalf("expected Policy matches section, got %q", out)
	}
	if !strings.Contains(out, "HIDDEN") || !strings.Contains(out, "server.pem") {
		t.Fatalf("expected hidden match, got %q", out)
	}
	if !strings.Contains(out, "MASKED") || !strings.Contains(out, ".env") || !strings.Contains(out, "env-value") {
		t.Fatalf("expected masked match with pattern name, got %q", out)
	}
}

func TestRunCheckMatchesJSONOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusfs.yml"), "version: 1\nhide:\n  - \"*.pem\"\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")

	out := captureStdout(t, func() {
		if err := runCheck(root, true, false, true); err != nil {
			t.Fatalf("runCheck returned error: %v", err)
		}
	})
	var report struct {
		Matches []struct {
			Decision string `json:"decision"`
			Path     string `json:"path"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(report.Matches) != 1 || report.Matches[0].Decision != "HIDDEN" || report.Matches[0].Path != "server.pem" {
		t.Fatalf("unexpected matches: %#v", report.Matches)
	}
}
