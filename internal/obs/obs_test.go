package obs

import (
	"testing"
)

func TestEventBusEmitAndDrop(t *testing.T) {
	b := NewEventBus(4)
	b.Emit(Event{Op: OpRead, Path: "test"})
	if b.Dropped() != 0 {
		t.Errorf("expected 0 dropped, got %d", b.Dropped())
	}
	// Fill the channel (capacity 4) with 5 more events — 2 should drop.
	for i := 0; i < 5; i++ {
		b.Emit(Event{})
	}
	if b.Dropped() != 2 {
		t.Errorf("expected 2 dropped, got %d", b.Dropped())
	}
	// Drain the 4 buffered events.
	for i := 0; i < 4; i++ {
		<-b.Events()
	}
}

func TestJanusMetricsRecordOps(t *testing.T) {
	m := &JanusMetrics{}
	m.RecordOp(OpRead, Allowed)
	m.RecordOp(OpRead, Allowed)
	m.RecordOp(OpRead, Masked)
	m.RecordOp(OpOpen, Hidden)

	s := m.Snapshot()
	if s.Ops["read:ALLOWED"] != 2 {
		t.Errorf("expected 2 ALLOWED reads, got %d", s.Ops["read:ALLOWED"])
	}
	if s.Ops["read:MASKED"] != 1 {
		t.Errorf("expected 1 MASKED read, got %d", s.Ops["read:MASKED"])
	}
	if s.Ops["open:HIDDEN"] != 1 {
		t.Errorf("expected 1 HIDDEN open, got %d", s.Ops["open:HIDDEN"])
	}
}

func TestJanusMetricsRecordBytes(t *testing.T) {
	m := &JanusMetrics{}
	m.RecordBytes(Allowed, 100)
	m.RecordBytes(Masked, 50)
	m.RecordBytes(Allowed, 200)

	s := m.Snapshot()
	if s.Bytes["ALLOWED"] != 300 {
		t.Errorf("expected 300 ALLOWED bytes, got %d", s.Bytes["ALLOWED"])
	}
	if s.Bytes["MASKED"] != 50 {
		t.Errorf("expected 50 MASKED bytes, got %d", s.Bytes["MASKED"])
	}
}

func TestRingBuffer(t *testing.T) {
	r := NewRingBuffer(4)
	r.Push("a")
	r.Push("b")
	r.Push("c")
	r.Push("d")

	snap := r.Snapshot()
	if len(snap) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(snap))
	}
	// After filling, push one more to wrap around.
	r.Push("e")
	snap = r.Snapshot()
	if len(snap) != 4 {
		t.Errorf("expected 4 entries after wrap, got %d", len(snap))
	}
	if snap[0] != "b" {
		t.Errorf("expected oldest 'b', got %q", snap[0])
	}
	if snap[3] != "e" {
		t.Errorf("expected newest 'e', got %q", snap[3])
	}
}

func TestTopN(t *testing.T) {
	tn := NewTopN(100)
	tn.Record("/a.txt", 100)
	tn.Record("/a.txt", 50)
	tn.Record("/b.txt", 200)

	top := tn.TopReads(10)
	if len(top) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(top))
	}
	if top[0].Path != "/a.txt" || top[0].Reads != 2 {
		t.Errorf("expected /a.txt with 2 reads, got %s %d", top[0].Path, top[0].Reads)
	}
	if top[1].Path != "/b.txt" || top[1].Reads != 1 {
		t.Errorf("expected /b.txt with 1 read, got %s %d", top[1].Path, top[1].Reads)
	}
}

func TestTopBytes(t *testing.T) {
	tn := NewTopN(100)
	tn.Record("/big.txt", 1000)
	tn.Record("/small.txt", 100)

	top := tn.TopBytes(10)
	if len(top) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(top))
	}
	if top[0].Path != "/big.txt" || top[0].Bytes != 1000 {
		t.Errorf("expected /big.txt with 1000 bytes, got %s %d", top[0].Path, top[0].Bytes)
	}
}

func TestLatencyHistogram(t *testing.T) {
	m := &JanusMetrics{}
	for i := 0; i < 100; i++ {
		m.RecordLatency(OpRead, int64(i))
	}
	snaps := m.LatencySnapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 latency snapshot, got %d", len(snaps))
	}
	if snaps[0].Op != "read" {
		t.Errorf("expected 'read' op, got %q", snaps[0].Op)
	}
	if snaps[0].Hits != 100 {
		t.Errorf("expected 100 hits, got %d", snaps[0].Hits)
	}
}
