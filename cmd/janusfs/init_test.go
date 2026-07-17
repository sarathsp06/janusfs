package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInit_WritesBothTemplates(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, false); err != nil {
		t.Fatalf("runInit() error = %v", err)
	}

	ignore, err := os.ReadFile(filepath.Join(dir, ".janusignore"))
	if err != nil {
		t.Fatalf("reading .janusignore: %v", err)
	}
	if !strings.Contains(string(ignore), "*.pem") {
		t.Error(".janusignore missing expected *.pem entry")
	}

	mask, err := os.ReadFile(filepath.Join(dir, ".janusmask"))
	if err != nil {
		t.Fatalf("reading .janusmask: %v", err)
	}
	if !strings.Contains(string(mask), "env-value") {
		t.Error(".janusmask missing expected env-value pattern")
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
	if _, err := os.Stat(filepath.Join(wantDir, ".janusignore")); err != nil {
		t.Errorf("expected .janusignore under %s: %v", wantDir, err)
	}
	if _, err := os.Stat(filepath.Join(wantDir, ".janusmask")); err != nil {
		t.Errorf("expected .janusmask under %s: %v", wantDir, err)
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
