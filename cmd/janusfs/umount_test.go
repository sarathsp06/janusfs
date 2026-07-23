package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/mount"
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

func TestUnmountKernel_ForceFallbackAfterGracefulFailures(t *testing.T) {
	old := unmountCommand
	t.Cleanup(func() { unmountCommand = old })

	var calls []string
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "diskutil" && reflect.DeepEqual(args, []string{"unmount", "force", "/mnt/busy"}) {
			return nil
		}
		return fmt.Errorf("busy")
	}

	if err := unmountKernel("/mnt/busy", true); err != nil {
		t.Fatalf("unmountKernel() error = %v, want nil after force fallback", err)
	}

	want := []string{
		"diskutil unmount /mnt/busy",
		"umount /mnt/busy",
		"diskutil unmount force /mnt/busy",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unmount attempts = %v, want %v", calls, want)
	}
}

func TestMountRuntimeStop_ForceUnmountsWhenServeLoopDoesNotExit(t *testing.T) {
	oldGrace := shutdownGrace
	oldSettle := forceUnmountSettle
	oldCommand := unmountCommand
	t.Cleanup(func() {
		shutdownGrace = oldGrace
		forceUnmountSettle = oldSettle
		unmountCommand = oldCommand
	})
	shutdownGrace = time.Millisecond
	forceUnmountSettle = time.Millisecond

	var forceCalled bool
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		if name == "diskutil" && reflect.DeepEqual(args, []string{"unmount", "force", "/mnt/stuck"}) {
			forceCalled = true
			return nil
		}
		return fmt.Errorf("busy")
	}

	rt := &mountRuntime{
		Mountpoint: "/mnt/stuck",
		adapter:    &mount.Adapter{},
		done:       make(chan struct{}),
	}
	rt.stop()

	if !forceCalled {
		t.Fatal("stop() did not force-unmount a mount whose serve loop failed to exit")
	}
}
