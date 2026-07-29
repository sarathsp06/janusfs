package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit_WritesPolicyTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false); err != nil {
		t.Fatalf("runInit() error = %v", err)
	}

	policy, err := os.ReadFile(filepath.Join(dir, ".janusfs.yml"))
	if err != nil {
		t.Fatalf("reading .janusfs.yml: %v", err)
	}
	if !strings.Contains(string(policy), "*.pem") {
		t.Error(".janusfs.yml missing expected *.pem entry")
	}
	if !strings.Contains(string(policy), "env-value") {
		t.Error(".janusfs.yml missing expected env-value pattern")
	}
}

func TestRunInit_RefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false); err != nil {
		t.Fatalf("first runInit() error = %v", err)
	}
	if err := runInit(dir, false); err == nil {
		t.Fatal("second runInit() without --force = nil error, want refusal")
	}
}

func TestRunInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false); err != nil {
		t.Fatalf("first runInit() error = %v", err)
	}
	if err := runInit(dir, true); err != nil {
		t.Errorf("runInit() with --force error = %v, want nil", err)
	}
}

func TestRunInitGlobal_WritesUnderHomeJanusfsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runInitGlobal(false); err != nil {
		t.Fatalf("runInitGlobal() error = %v", err)
	}

	wantDir := filepath.Join(home, ".janusfs", "config")
	if _, err := os.Stat(filepath.Join(wantDir, ".janusfs.yml")); err != nil {
		t.Errorf("expected .janusfs.yml under %s: %v", wantDir, err)
	}
}

func TestRunInitGlobal_RefusesToOverwriteWithoutForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runInitGlobal(false); err != nil {
		t.Fatalf("first runInitGlobal() error = %v", err)
	}
	if err := runInitGlobal(false); err == nil {
		t.Fatal("second runInitGlobal() without --force = nil error, want refusal")
	}
}
