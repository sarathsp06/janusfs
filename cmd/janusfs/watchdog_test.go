package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/config"
)

// withWatchdogTestHooks overrides the package-level indirection points used by
// the watchdog (mirroring the existing unmountCommand/mountpointMounted
// pattern in umount.go) so tests can drive it without any real process or
// mount, and restores the originals afterward.
func withWatchdogTestHooks(t *testing.T, alive func(pid int) bool, mounted func(path string) bool) {
	t.Helper()
	origAlive := watchdogPidAlive
	origMounted := mountpointMounted
	origUnmount := unmountCommand
	watchdogPidAlive = alive
	mountpointMounted = mounted
	unmountCommand = func(name string, args []string, timeoutSec int) error { return nil }
	t.Cleanup(func() {
		watchdogPidAlive = origAlive
		mountpointMounted = origMounted
		unmountCommand = origUnmount
	})
}

func TestSweepStaleMounts_OnlySweepsMountedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := config.RecordMount("/src/a", "/mnt/a", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordMount("/src/b", "/mnt/b", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordMount("/src/c", "/mnt/c", ""); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var unmounted []string
	stillMounted := map[string]bool{"/mnt/b": true} // only b is still actually mounted

	origUnmount := unmountCommand
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			unmounted = append(unmounted, args[len(args)-1])
		}
		return nil
	}
	origMounted := mountpointMounted
	mountpointMounted = func(path string) bool { return stillMounted[path] }
	t.Cleanup(func() {
		unmountCommand = origUnmount
		mountpointMounted = origMounted
	})

	sweepStaleMounts(nil)

	mu.Lock()
	defer mu.Unlock()
	if len(unmounted) != 1 || unmounted[0] != "/mnt/b" {
		t.Fatalf("expected exactly one unmount call for /mnt/b, got %v", unmounted)
	}
}

func TestSweepStaleMounts_CleanShutdownIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := config.RecordMount("/src/a", "/mnt/a", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordMount("/src/b", "/mnt/b", ""); err != nil {
		t.Fatal(err)
	}

	calls := 0
	origUnmount := unmountCommand
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		calls++
		return nil
	}
	origMounted := mountpointMounted
	mountpointMounted = func(path string) bool { return false } // everything already unmounted
	t.Cleanup(func() {
		unmountCommand = origUnmount
		mountpointMounted = origMounted
	})

	sweepStaleMounts(nil)

	if calls != 0 {
		t.Fatalf("expected zero unmount calls on a clean shutdown, got %d", calls)
	}
}

func TestRunWatchdog_ExitsOnCancelWithoutSweeping(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.RecordMount("/src/a", "/mnt/a", ""); err != nil {
		t.Fatal(err)
	}

	calls := 0
	withWatchdogTestHooks(t,
		func(pid int) bool { return true }, // supervised pid stays alive throughout
		func(path string) bool { return true },
	)
	origUnmount := unmountCommand
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		calls++
		return nil
	}
	t.Cleanup(func() { unmountCommand = origUnmount })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWatchdog(ctx, 12345, 10*time.Millisecond) }()

	time.Sleep(30 * time.Millisecond) // let a few poll ticks pass while "alive"
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWatchdog returned error on cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runWatchdog did not exit after context cancellation")
	}

	if calls != 0 {
		t.Fatalf("expected zero unmount calls when the daemon never died, got %d", calls)
	}
}

func TestRunWatchdog_SweepsOnceDaemonDies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.RecordMount("/src/a", "/mnt/a", ""); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	aliveTicks := 0
	withWatchdogTestHooks(t,
		func(pid int) bool {
			mu.Lock()
			defer mu.Unlock()
			aliveTicks++
			// Alive for the startup check and first couple of ticks, then dead.
			return aliveTicks <= 3
		},
		func(path string) bool { return true },
	)
	calls := 0
	origUnmount := unmountCommand
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		calls++
		return nil
	}
	t.Cleanup(func() { unmountCommand = origUnmount })

	err := runWatchdog(context.Background(), 12345, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("runWatchdog returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one unmount call once the daemon died, got %d", calls)
	}
}

func TestRunWatchdog_RefusesDeadOrInvalidPid(t *testing.T) {
	withWatchdogTestHooks(t, func(pid int) bool { return false }, func(path string) bool { return false })

	if err := runWatchdog(context.Background(), 12345, time.Second); err == nil {
		t.Fatal("expected an error when the supervised pid is not alive at startup")
	}
	if err := runWatchdog(context.Background(), 0, time.Second); err == nil {
		t.Fatal("expected an error for pid 0")
	}
	if err := runWatchdog(context.Background(), -1, time.Second); err == nil {
		t.Fatal("expected an error for a negative pid")
	}
}
