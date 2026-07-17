package config

import (
	"os"
	"path/filepath"
	"testing"
)

func newCfg(t *testing.T, src, mountpoint string) Config {
	t.Helper()
	c := Default()
	c.Src = src
	c.Mountpoint = mountpoint
	return c
}

func TestValidate_ValidDistinctPaths(t *testing.T) {
	src := t.TempDir()
	mount := t.TempDir()

	c := newCfg(t, src, mount)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_MissingSrc(t *testing.T) {
	mount := t.TempDir()
	c := newCfg(t, filepath.Join(mount, "does-not-exist"), mount)

	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for missing src")
	}
}

func TestValidate_MissingMountpoint(t *testing.T) {
	src := t.TempDir()
	c := newCfg(t, src, filepath.Join(src, "does-not-exist"))

	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for missing mountpoint")
	}
}

func TestValidate_NonEmptyMountpoint(t *testing.T) {
	src := t.TempDir()
	mount := t.TempDir()
	if err := os.WriteFile(filepath.Join(mount, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newCfg(t, src, mount)
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for non-empty mountpoint")
	}
}

func TestValidate_MountpointSubdirOfSrc(t *testing.T) {
	src := t.TempDir()
	mount := filepath.Join(src, "mnt")
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatal(err)
	}

	c := newCfg(t, src, mount)
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error when mountpoint is a subdirectory of src")
	}
}

func TestValidate_SrcSubdirOfMountpoint(t *testing.T) {
	mount := t.TempDir()
	src := filepath.Join(mount, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}

	c := newCfg(t, src, mount)
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error when src is a subdirectory of mountpoint")
	}
}

func TestValidate_IdenticalPaths(t *testing.T) {
	dir := t.TempDir()
	c := newCfg(t, dir, dir)

	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error when src and mountpoint are identical")
	}
}

func TestValidate_EmptyFields(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error when src/mountpoint are unset")
	}
}

func TestApplyEnv_OverridesDefaultWhenSet(t *testing.T) {
	t.Setenv("JANUSFS_UI_PORT", "8000")
	t.Setenv("JANUSFS_CACHE_MAX_BYTES", "1000")

	cfg := Default()
	if err := ApplyEnv(&cfg); err != nil {
		t.Fatalf("ApplyEnv() error = %v", err)
	}
	if cfg.UIPort != 8000 {
		t.Errorf("UIPort = %d, want 8000 (from env)", cfg.UIPort)
	}
	if cfg.CacheMaxBytes != 1000 {
		t.Errorf("CacheMaxBytes = %d, want 1000 (from env)", cfg.CacheMaxBytes)
	}
}

func TestApplyEnv_LeavesUnsetFieldsAtDefault(t *testing.T) {
	// No env vars set for these fields at all.
	cfg := Default()
	if err := ApplyEnv(&cfg); err != nil {
		t.Fatalf("ApplyEnv() error = %v", err)
	}
	if cfg.UIPort != DefaultUIPort {
		t.Errorf("UIPort = %d, want untouched default %d", cfg.UIPort, DefaultUIPort)
	}
	if cfg.CacheMaxBytes != DefaultCacheMaxBytes {
		t.Errorf("CacheMaxBytes = %d, want untouched default %d", cfg.CacheMaxBytes, DefaultCacheMaxBytes)
	}
	if cfg.RedactBufferMax != DefaultRedactBufferMax {
		t.Errorf("RedactBufferMax = %d, want untouched default %d", cfg.RedactBufferMax, DefaultRedactBufferMax)
	}
}

func TestApplyEnv_NoHistoryBoolFromEnv(t *testing.T) {
	t.Setenv("JANUSFS_NO_HISTORY", "true")

	cfg := Default()
	if err := ApplyEnv(&cfg); err != nil {
		t.Fatalf("ApplyEnv() error = %v", err)
	}
	if !cfg.NoHistory {
		t.Error("NoHistory = false, want true (from env)")
	}
}

func TestApplyEnv_DoesNotTouchPositionals(t *testing.T) {
	// Src/Mountpoint are tagged env:"-": ApplyEnv must never populate them
	// even if an env var happened to be named JANUSFS_SRC etc.
	t.Setenv("JANUSFS_SRC", "/should/not/appear")

	cfg := Default()
	cfg.Src = "/original"
	if err := ApplyEnv(&cfg); err != nil {
		t.Fatalf("ApplyEnv() error = %v", err)
	}
	if cfg.Src != "/original" {
		t.Errorf("Src = %q, want unchanged %q", cfg.Src, "/original")
	}
}

func TestDefault_Values(t *testing.T) {
	c := Default()
	if c.UIPort != 7381 {
		t.Errorf("UIPort = %d, want 7381", c.UIPort)
	}
	if c.CacheMaxBytes != 256*1024*1024 {
		t.Errorf("CacheMaxBytes = %d, want 256MB", c.CacheMaxBytes)
	}
	if c.CacheMaxFile != 64*1024*1024 {
		t.Errorf("CacheMaxFile = %d, want 64MB", c.CacheMaxFile)
	}
	if c.HistoryRetentionDays != 30 {
		t.Errorf("HistoryRetentionDays = %d, want 30", c.HistoryRetentionDays)
	}
	if c.NoHistory != false {
		t.Errorf("NoHistory = %v, want false", c.NoHistory)
	}
	if c.RedactBufferMax != 512*1024*1024 {
		t.Errorf("RedactBufferMax = %d, want 512MB", c.RedactBufferMax)
	}
}
