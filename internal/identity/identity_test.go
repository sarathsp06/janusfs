package identity

import (
	"os"
	"testing"
)

func TestIdentityAndRegistry(t *testing.T) {
	pid := os.Getpid()
	ppid, start, err := getOSProcessInfo(pid)
	if err != nil {
		t.Fatalf("failed to get OS process info: %v", err)
	}

	t.Logf("pid: %d, ppid: %d, start: %d", pid, ppid, start)

	chainHash, err := GetPPIDChain(pid)
	if err != nil {
		t.Fatalf("failed to get PPID chain: %v", err)
	}

	bootUUID, err := GetBootUUID()
	if err != nil {
		t.Fatalf("failed to get boot UUID: %v", err)
	}

	id := Identity{
		PID:           pid,
		StartTime:     start,
		PPIDChainHash: chainHash,
		BootUUID:      bootUUID,
	}

	reg := NewRegistry()
	if !reg.IsEmpty() {
		t.Errorf("expected registry to be empty initially")
	}

	reg.Register(pid, id)
	if reg.IsEmpty() {
		t.Errorf("expected registry to not be empty after registration")
	}

	if !reg.Verify(pid) {
		t.Errorf("expected verification to succeed for registered PID")
	}

	// Verify that unregister works
	reg.Unregister(pid)
	if !reg.IsEmpty() {
		t.Errorf("expected registry to be empty after unregister")
	}
}

func TestRegistryPIDReuse(t *testing.T) {
	reg := NewRegistry()

	// Register a dummy PID with a dummy start time
	dummyPID := 99999
	id := Identity{
		PID:           dummyPID,
		StartTime:     12345, // dummy start time
		PPIDChainHash: "dummy-hash",
		BootUUID:      "dummy-uuid",
	}

	reg.Register(dummyPID, id)
	if reg.IsEmpty() {
		t.Fatalf("expected registry to not be empty")
	}

	// Verify should fail because the actual process 99999 is either not running
	// or has a different start time, which triggers immediate purging!
	if reg.Verify(dummyPID) {
		t.Errorf("expected verification to fail for recycled/dummy PID")
	}

	// It should have been purged!
	if !reg.IsEmpty() {
		t.Errorf("expected registry to be purged after failed verification")
	}
}
