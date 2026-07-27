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

func TestResolveMountpoint_NoOpWhenMountpointSet(t *testing.T) {
	c := Default()
	c.Src = "/src"
	c.Mountpoint = "/explicit"
	c.MountRoot = t.TempDir()

	if err := c.ResolveMountpoint(); err != nil {
		t.Fatalf("ResolveMountpoint() error = %v", err)
	}
	if c.Mountpoint != "/explicit" {
		t.Errorf("Mountpoint = %q, want unchanged %q (explicit wins)", c.Mountpoint, "/explicit")
	}
}

func TestResolveMountpoint_NoOpWhenMountRootUnset(t *testing.T) {
	c := Default()
	c.Src = "/src"

	if err := c.ResolveMountpoint(); err != nil {
		t.Fatalf("ResolveMountpoint() error = %v", err)
	}
	if c.Mountpoint != "" {
		t.Errorf("Mountpoint = %q, want empty (derivation disabled)", c.Mountpoint)
	}
}

func TestResolveMountpoint_MirrorsFullSrcPath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "myproject")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}

	c := Default()
	c.Src = src
	c.MountRoot = root

	if err := c.ResolveMountpoint(); err != nil {
		t.Fatalf("ResolveMountpoint() error = %v", err)
	}
	srcAbs, err := absClean(src)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, srcAbs)
	if c.Mountpoint != want {
		t.Errorf("Mountpoint = %q, want %q", c.Mountpoint, want)
	}
	if info, err := os.Stat(c.Mountpoint); err != nil || !info.IsDir() {
		t.Errorf("derived mountpoint %q was not created as a directory: %v", c.Mountpoint, err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() after derivation = %v, want nil", err)
	}
}

func TestResolveMountpoint_DistinctSourcesDontCollide(t *testing.T) {
	root := t.TempDir()
	srcA := filepath.Join(t.TempDir(), "shared")
	srcB := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(srcA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(srcB, 0o755); err != nil {
		t.Fatal(err)
	}

	a := Default()
	a.Src = srcA
	a.MountRoot = root
	if err := a.ResolveMountpoint(); err != nil {
		t.Fatalf("first ResolveMountpoint() error = %v", err)
	}

	b := Default()
	b.Src = srcB
	b.MountRoot = root
	if err := b.ResolveMountpoint(); err != nil {
		t.Fatalf("second ResolveMountpoint() error = %v", err)
	}

	if a.Mountpoint == b.Mountpoint {
		t.Fatalf("distinct sources derived the same mountpoint: %q", a.Mountpoint)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("a.Validate() = %v, want nil", err)
	}
	if err := b.Validate(); err != nil {
		t.Errorf("b.Validate() = %v, want nil", err)
	}
}

func TestApplyFile_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SaveSettings("/some/mount/root"); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}

	cfg := Default()
	if err := ApplyFile(&cfg); err != nil {
		t.Fatalf("ApplyFile() error = %v", err)
	}
	if cfg.MountRoot != "/some/mount/root" {
		t.Errorf("MountRoot = %q, want %q", cfg.MountRoot, "/some/mount/root")
	}
}

func TestApplyFile_MissingIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := Default()
	if err := ApplyFile(&cfg); err != nil {
		t.Fatalf("ApplyFile() error = %v, want nil for missing settings file", err)
	}
	if cfg.MountRoot != "" {
		t.Errorf("MountRoot = %q, want unchanged empty", cfg.MountRoot)
	}
}

func TestMountsRegistry_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Empty registry: LoadMounts is a no-op, RemoveMount is harmless.
	if recs, err := LoadMounts(); err != nil || len(recs) != 0 {
		t.Fatalf("LoadMounts() on empty = (%v, %v), want (nil, nil)", recs, err)
	}
	if err := RemoveMount("/nothing"); err != nil {
		t.Fatalf("RemoveMount on empty registry = %v, want nil", err)
	}

	if err := RecordMount("/src/a", "/mnt/a", "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := RecordMount("/src/b", "/mnt/b", ""); err != nil {
		t.Fatal(err)
	}
	// Re-record the same mountpoint: upsert, not duplicate; src + label updated.
	if err := RecordMount("/src/a2", "/mnt/a", "alpha2"); err != nil {
		t.Fatal(err)
	}

	recs, err := LoadMounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("registry has %d entries, want 2 (upsert should not duplicate): %+v", len(recs), recs)
	}
	got := map[string]MountRecord{}
	for _, r := range recs {
		got[r.Mountpoint] = r
	}
	if got["/mnt/a"].Src != "/src/a2" || got["/mnt/a"].Label != "alpha2" {
		t.Errorf("upsert did not update record: /mnt/a -> %+v, want src /src/a2 label alpha2", got["/mnt/a"])
	}

	if err := RemoveMount("/mnt/a"); err != nil {
		t.Fatal(err)
	}
	recs, _ = LoadMounts()
	if len(recs) != 1 || recs[0].Mountpoint != "/mnt/b" {
		t.Errorf("after RemoveMount, registry = %+v, want only /mnt/b", recs)
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
