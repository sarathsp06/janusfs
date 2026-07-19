package obs

import (
	"sync/atomic"
)

// EventBus is an MPSC (multi-producer, single-consumer) channel for events
// (SPEC §10: "single MPSC channel (cap 4096) drained by one fan-out goroutine").
// Producers call Emit (non-blocking); the consumer goroutine calls Consume.
type EventBus struct {
	ch          chan Event
	dropped     atomic.Uint64
}

// NewEventBus creates an event bus with the given buffer capacity.
func NewEventBus(capacity int) *EventBus {
	return &EventBus{ch: make(chan Event, capacity)}
}

// Emit sends an event via non-blocking send. If the channel is full the
// event is dropped and the dropped counter is incremented — FUSE handlers
// never block on observability (NFR-5).
func (b *EventBus) Emit(e Event) {
	select {
	case b.ch <- e:
	default:
		b.dropped.Add(1)
	}
}

// Events returns the receive channel for the consumer goroutine.
func (b *EventBus) Events() <-chan Event {
	return b.ch
}

// Dropped returns the number of events dropped due to a full channel.
func (b *EventBus) Dropped() uint64 {
	return b.dropped.Load()
}

// Close shuts down the channel so the consumer goroutine can exit.
func (b *EventBus) Close() {
	close(b.ch)
}
