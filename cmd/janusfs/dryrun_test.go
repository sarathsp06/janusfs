package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarathsp06/janusfs/internal/check"
	"github.com/sarathsp06/janusfs/internal/execrunner"
)

func TestPrintDryRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".janusfs.yml"), []byte("version: 1\nhide: ['*.pem']\nmask:\n  - paths: ['.env']\n    patterns: [env-value]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.pem", ".env"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x=y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := execrunner.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	ins, err := execrunner.Inspect(plan, "none", ".janusfs.yml exec.net")
	if err != nil {
		t.Fatal(err)
	}
	ins.GitStageable = []string{".env"}
	ins.Findings = append(ins.Findings, check.Finding{Severity: check.SeverityError, Message: "bad regex in rule 3"})

	var buf bytes.Buffer
	printDryRun(&buf, ins, "exec: note: overridden")
	out := buf.String()
	for _, want := range []string{
		"nothing was run",
		"none (loopback only) · from .janusfs.yml exec.net",
		"overridden",
		"1 hidden, 1 masked",
		"a.pem",
		".env",
		"[env-value]",
		"stageable by git",
		"1 policy error(s)",
		"bad regex in rule 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if !ins.Enforcement.Available && !strings.Contains(out, "NOT available here") {
		t.Errorf("unavailable enforcement must be stated:\n%s", out)
	}
}
