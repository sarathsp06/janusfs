package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPidfilePath_StableForSameMountpoint(t *testing.T) {
	mount := t.TempDir()
	p1, err := pidfilePath(mount)
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	p2, err := pidfilePath(mount)
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	if p1 != p2 {
		t.Errorf("pidfilePath not stable: %q != %q", p1, p2)
	}
}

func TestPidfilePath_DiffersForDifferentMountpoints(t *testing.T) {
	p1, err := pidfilePath(t.TempDir())
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	p2, err := pidfilePath(t.TempDir())
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	if p1 == p2 {
		t.Errorf("pidfilePath collided for distinct mountpoints: %q", p1)
	}
}

func TestWriteReadRemovePidfile_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mount := t.TempDir()

	if err := writePidfile(mount); err != nil {
		t.Fatalf("writePidfile() error = %v", err)
	}

	path, err := pidfilePath(mount)
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat run dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("run dir perm = %o, want 0700", perm)
	}

	pid, err := readPidfile(mount)
	if err != nil {
		t.Fatalf("readPidfile() error = %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("readPidfile() = %d, want %d", pid, os.Getpid())
	}

	if err := removePidfile(mount); err != nil {
		t.Fatalf("removePidfile() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pidfile still exists after removePidfile()")
	}
}

func TestWritePidfile_SecondLineIsMountpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mount := t.TempDir()

	if err := writePidfile(mount); err != nil {
		t.Fatalf("writePidfile() error = %v", err)
	}

	path, err := pidfilePath(mount)
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading pidfile: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (pid, mountpoint), got %d: %q", len(lines), data)
	}
	if lines[0] != strconv.Itoa(os.Getpid()) {
		t.Errorf("first line = %q, want pid %d", lines[0], os.Getpid())
	}
	absMount, _ := filepath.Abs(mount)
	if lines[1] != absMount {
		t.Errorf("second line = %q, want absolute mountpoint %q", lines[1], absMount)
	}

	// readPidfile must still parse only the first line correctly.
	pid, err := readPidfile(mount)
	if err != nil {
		t.Fatalf("readPidfile() error = %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("readPidfile() = %d, want %d", pid, os.Getpid())
	}
}

func TestReadPidfile_LegacySingleLineStillParses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mount := t.TempDir()

	path, err := pidfilePath(mount)
	if err != nil {
		t.Fatalf("pidfilePath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A pidfile written before mountpoint recording existed: PID only, no
	// trailing newline, no second line.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	pid, err := readPidfile(mount)
	if err != nil {
		t.Fatalf("readPidfile() error = %v, want nil for a legacy single-line pidfile", err)
	}
	if pid != os.Getpid() {
		t.Errorf("readPidfile() = %d, want %d", pid, os.Getpid())
	}
}

func TestReadPidfileMountpoint_ReadsSecondLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mount := t.TempDir()

	if err := writePidfile(mount); err != nil {
		t.Fatalf("writePidfile() error = %v", err)
	}
	path, err := pidfilePath(mount)
	if err != nil {
		t.Fatal(err)
	}

	absMount, _ := filepath.Abs(mount)
	if got := readPidfileMountpoint(path); got != absMount {
		t.Errorf("readPidfileMountpoint() = %q, want %q", got, absMount)
	}
}

func TestReadPidfileMountpoint_LegacyFileReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mount := t.TempDir()

	path, err := pidfilePath(mount)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readPidfileMountpoint(path); got != "" {
		t.Errorf("readPidfileMountpoint() on legacy file = %q, want empty", got)
	}
}

func TestReadPidfile_MissingIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pid, err := readPidfile(t.TempDir())
	if err != nil {
		t.Fatalf("readPidfile() error = %v, want nil for missing pidfile", err)
	}
	if pid != 0 {
		t.Errorf("readPidfile() = %d, want 0 for missing pidfile", pid)
	}
}

func TestRemovePidfile_MissingIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := removePidfile(t.TempDir()); err != nil {
		t.Errorf("removePidfile() error = %v, want nil for missing pidfile", err)
	}
}

func TestPruneMirrorDirs(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "Users", "me", "projects", "app")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	pruneMirrorDirs(deep, root)
	if _, err := os.Stat(filepath.Join(root, "Users")); !os.IsNotExist(err) {
		t.Errorf("expected empty chain up to root to be removed, got err=%v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Errorf("root %q should never be removed", root)
	}
}

func TestPruneMirrorDirs_StopsAtNonEmptySibling(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "Users", "me", "app")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Users", "me", "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneMirrorDirs(deep, root)
	if _, err := os.Stat(filepath.Join(root, "Users", "me")); err != nil {
		t.Errorf("non-empty parent should survive, stat err=%v", err)
	}
	if _, err := os.Stat(deep); !os.IsNotExist(err) {
		t.Errorf("expected leaf dir to be removed, got err=%v", err)
	}
}

func TestPruneMirrorDirs_OutsideRootUntouched(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	pruneMirrorDirs(outside, root)
	if info, err := os.Stat(outside); err != nil || !info.IsDir() {
		t.Errorf("path outside root should be untouched, err=%v", err)
	}
}
