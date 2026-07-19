package health

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRunBasic(t *testing.T) {
	r := Run("")
	if r == nil {
		t.Fatal("expected non-nil report")
	}
	if r.Runtime.GoVersion == "" {
		t.Error("expected Go version")
	}
	if r.Runtime.NumCPU <= 0 {
		t.Error("expected positive NumCPU")
	}
}

func TestRunWithPidfileDir(t *testing.T) {
	dir := t.TempDir()

	// Write a live pidfile (our own PID).
	pidPath := filepath.Join(dir, "testmount.pid")
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600)

	// Write a stale pidfile.
	stalePath := filepath.Join(dir, "stale.pid")
	os.WriteFile(stalePath, []byte("99999999"), 0o600)

	r := Run(dir)
	if r == nil {
		t.Fatal("expected non-nil report")
	}

	foundLive := false
	foundStale := false
	for _, m := range r.Mounts {
		if m.Mountpoint == "testmount" {
			foundLive = true
			if !m.Alive {
				t.Error("expected testmount to be alive")
			}
		}
		if m.Mountpoint == "stale" {
			foundStale = true
			if m.Alive {
				t.Error("expected stale mount to be dead")
			}
		}
	}
	if !foundLive {
		t.Error("expected testmount in mount list")
	}
	if !foundStale {
		t.Error("expected stale in mount list")
	}

	if len(r.Warnings) == 0 {
		t.Error("expected at least one warning (stale pidfile)")
	}
}

func TestMacFUSEStatus(t *testing.T) {
	s := checkMacFUSE()
	// On a CI machine or dev machine without macFUSE, this will be false.
	// Just check that it doesn't panic and returns a valid struct.
	_ = s.Installed
	_ = s.Loaded
}

func TestPidAlive(t *testing.T) {
	// Our own PID should be alive.
	if !pidAlive(os.Getpid()) {
		t.Error("expected our own PID to be alive")
	}
	// PID 0 should not be alive (or returns error).
	if pidAlive(0) {
		t.Log("PID 0 reported as alive (permissive platform)")
	}
}
