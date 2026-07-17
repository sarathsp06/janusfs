package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRunUmount_NoMountReturnsError(t *testing.T) {
	// runUmount always tries fuse.Unmount first; an OS-level unmount of a
	// directory that isn't a mount point returns an error, which is what
	// we surface. That contract is what this test pins.
	dir := t.TempDir()
	if err := runUmount(dir); err == nil {
		t.Fatal("expected an error unmounting a directory that isn't a mount")
	}
}

func TestRunUmount_RemovesStalePidfile(t *testing.T) {
	// Even when the OS-level unmount fails (no mount here), a stale
	// pidfile should not survive — the whole point of FR-3's pidfile is
	// discoverability, and a leftover from a crashed prior run must not
	// mislead future umount calls.
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	pf, err := pidfilePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pf), 0o700); err != nil {
		t.Fatal(err)
	}
	// A definitely-invalid PID so we never actually signal a real process.
	if err := os.WriteFile(pf, []byte(strconv.Itoa(999999999)), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = runUmount(dir) // error expected (nothing is mounted), but ignore it here

	if _, err := os.Stat(pf); !os.IsNotExist(err) {
		t.Errorf("expected pidfile at %s to be removed, stat err = %v", pf, err)
	}
}
