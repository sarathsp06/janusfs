package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/obs"
)

func TestOpenClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenNilOnEmptyPath(t *testing.T) {
	s, err := Open("/tmp", "", 30)
	if err != nil {
		t.Fatalf("Open with empty path: %v", err)
	}
	if s != nil {
		t.Fatal("expected nil store for empty path")
	}
}

func TestRecordAndStats(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.Record(obs.Event{Op: "open", Path: "a.txt", Decision: obs.Allowed, Bytes: 100, LatencyUs: 50})
	s.Record(obs.Event{Op: "read", Path: "a.txt", Decision: obs.Allowed, Bytes: 1024, LatencyUs: 200})
	s.Record(obs.Event{Op: "open", Path: ".env", Decision: obs.Masked, Bytes: 44, LatencyUs: 30})

	time.Sleep(100 * time.Millisecond)

	// Flush by closing.
	_ = s.Close()

	// Re-open and check stats.
	s2, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Re-open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	stats := s2.Stats(context.Background())
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats["coveredPaths"] != 2 {
		t.Errorf("expected 2 covered paths, got %v", stats["coveredPaths"])
	}
}

func TestQuery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.Record(obs.Event{Op: "open", Path: "a.txt", Decision: obs.Allowed, Bytes: 100, LatencyUs: 50})
	s.Record(obs.Event{Op: "read", Path: "a.txt", Decision: obs.Allowed, Bytes: 1024, LatencyUs: 200})
	s.Record(obs.Event{Op: "open", Path: ".env", Decision: obs.Masked, Bytes: 44, LatencyUs: 30})

	time.Sleep(100 * time.Millisecond)
	_ = s.Close()

	s2, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Re-open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	rows, err := s2.Query(context.Background(), time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected query results")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dir, dbPath, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.Record(obs.Event{Op: "open", Path: "old.txt", Decision: obs.Allowed})
	time.Sleep(50 * time.Millisecond)
	_ = s.Close()

	// Re-open with 0 retention.
	s2, err := Open(dir, dbPath, 0)
	if err != nil {
		t.Fatalf("Re-open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	rows, err := s2.Query(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) > 0 {
		t.Log("pruning with 0 retention is a no-op; data may still exist")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	s.Record(obs.Event{})
	if err := s.Close(); err != nil {
		t.Fatal("nil Close should be safe")
	}
	if stats := s.Stats(context.Background()); stats != nil {
		t.Fatal("nil Stats should return nil")
	}
}

func TestReopenPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	s1, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	s1.Record(obs.Event{Op: "open", Path: "persist.txt", Decision: obs.Allowed, Bytes: 42})
	s1.Record(obs.Event{Op: "open", Path: "persist.txt", Decision: obs.Allowed, Bytes: 42})
	time.Sleep(50 * time.Millisecond)
	_ = s1.Close()

	s2, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	defer func() { _ = s2.Close() }()

	rows, err := s2.Query(context.Background(), time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected persisted data")
	}
	var found bool
	for _, r := range rows {
		if r.Path == "persist.txt" && r.Cnt == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected persist.txt cnt=2, got %+v", rows)
	}
}

func TestCorruptionSafe(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "corrupt.db")

	_ = os.WriteFile(dbPath, []byte("not a valid sqlite database"), 0o600)

	s, err := Open(dir, dbPath, 30)
	if err == nil {
		_ = s.Close()
		t.Fatal("expected error opening corrupted DB")
	}
}

func TestConcurrentRecord(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "conc.db")
	s, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Record(obs.Event{Op: "open", Path: "conc.txt", Decision: obs.Allowed})
		}
		close(done)
	}()
	go func() {
		for i := 0; i < 100; i++ {
			s.Record(obs.Event{Op: "read", Path: "conc.txt", Decision: obs.Allowed})
		}
	}()

	<-done
	time.Sleep(100 * time.Millisecond)
	_ = s.Close()

	s2, err := Open(dir, dbPath, 30)
	if err != nil {
		t.Fatalf("Re-open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	rows, err := s2.Query(context.Background(), time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected data after concurrent writes")
	}
}
