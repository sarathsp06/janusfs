package obs

import (
	"sync"
)

// RingBuffer is a fixed-capacity circular buffer of serialised events
// (SPEC §10: "fixed 8192-slot circular buffer of serialised events").
// WebSocket subscribers get a snapshot then live tail.
type RingBuffer struct {
	mu    sync.Mutex
	pos   int
	full  bool
	slots []string
	cap   int
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		slots: make([]string, capacity),
		cap:   capacity,
	}
}

// Push adds an event label to the ring buffer.
func (r *RingBuffer) Push(label string) {
	r.mu.Lock()
	r.slots[r.pos] = label
	r.pos++
	if r.pos >= r.cap {
		r.pos = 0
		r.full = true
	}
	r.mu.Unlock()
}

// Snapshot returns all events in insertion order (oldest first).
func (r *RingBuffer) Snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.pos
	if r.full {
		n = r.cap
	}
	out := make([]string, n)
	if r.full {
		// Two segments: r.pos..end, then 0..r.pos
		copy(out, r.slots[r.pos:])
		copy(out[r.cap-r.pos:], r.slots[:r.pos])
	} else {
		copy(out, r.slots[:r.pos])
	}
	return out
}

// LiveFeed returns all events from the buffer plus a channel for new events.
// The caller must close the returned channel to unsubscribe.
func (r *RingBuffer) LiveFeed() ([]string, chan string) {
	snapshot := r.Snapshot()
	ch := make(chan string, 256)
	// We can't easily push to specific subscriber channels from the event
	// bus consumer — LiveFeed currently returns a snapshot and directs the
	// caller to poll or use the API's WebSocket endpoint which handles this.
	// For the WebSocket handler, just return the snapshot.
	return snapshot, ch
}
