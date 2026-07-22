package obs

import (
	"sync"
	"sync/atomic"
)

// JanusMetrics implements SPEC §10's metrics catalogue (FR-23) as a plain
// struct with atomic counters and concurrent-safe histograms — no external
// dependency (the Prometheus-based design is deferred; the struct surface
// mirrors SPEC's named JanusMetrics so swapping to Prometheus client_golang
// in a future pass is mechanical, not architectural).
type JanusMetrics struct {
	OpsTotal         sync.Map // map[string]*atomic.Uint64 — key "op:decision"
	BytesServed      sync.Map // map[string]*atomic.Int64 — key decision
	CacheHit         atomic.Uint64
	CacheMiss        atomic.Uint64
	CacheRebuild     atomic.Uint64
	HandlerLatency   sync.Map // map[string]*latencyHist — key op
	Generation       atomic.Uint64
	ConfigReloads    atomic.Uint64
	WatcherEvents    atomic.Uint64
	WatcherOverflows atomic.Uint64
	EventsDropped    atomic.Uint64

	mu         sync.Mutex
	latBuckets map[string]*latencyHist
}

// latencyHist records p50/p90/p99 for a metric.
type latencyHist struct {
	mu   sync.Mutex
	vals []float64
}

func newLatencyHist() *latencyHist {
	return &latencyHist{vals: make([]float64, 0, 1024)}
}

func (h *latencyHist) record(us float64) {
	h.mu.Lock()
	h.vals = append(h.vals, us)
	if len(h.vals) > 4096 {
		h.vals = h.vals[len(h.vals)-2048:]
	}
	h.mu.Unlock()
}

// RecordOp increments the counter for an operation+decision pair.
func (m *JanusMetrics) RecordOp(op Op, d Decision) {
	m.opCounter(op, d).Add(1)
}

func (m *JanusMetrics) opCounter(op Op, d Decision) *atomic.Uint64 {
	key := string(op) + ":" + d.String()
	v, _ := m.OpsTotal.LoadOrStore(key, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

// RecordBytes increments the byte counter for a decision.
func (m *JanusMetrics) RecordBytes(d Decision, n int64) {
	m.byteCounter(d).Add(n)
}

func (m *JanusMetrics) byteCounter(d Decision) *atomic.Int64 {
	v, _ := m.BytesServed.LoadOrStore(d.String(), new(atomic.Int64))
	return v.(*atomic.Int64)
}

// RecordLatency records a handler latency in microseconds for an operation.
func (m *JanusMetrics) RecordLatency(op Op, us int64) {
	m.latencyHistogram(op).record(float64(us))
}

func (m *JanusMetrics) latencyHistogram(op Op) *latencyHist {
	key := string(op)
	v, _ := m.HandlerLatency.LoadOrStore(key, newLatencyHist())
	return v.(*latencyHist)
}

// LatencySnapshot holds percentile values for one operation.
type LatencySnapshot struct {
	Op   string  `json:"op"`
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	Avg  float64 `json:"avg"`
	Hits int     `json:"hits"`
}

// LatencySnapshots returns percentile data for all tracked operations.
func (m *JanusMetrics) LatencySnapshots() []LatencySnapshot {
	var out []LatencySnapshot
	m.HandlerLatency.Range(func(key, val any) bool {
		h := val.(*latencyHist)
		h.mu.Lock()
		vals := make([]float64, len(h.vals))
		copy(vals, h.vals)
		h.mu.Unlock()
		if len(vals) == 0 {
			return true
		}
		sortFloat64s(vals)
		p50 := vals[len(vals)*50/100]
		p90 := vals[len(vals)*90/100]
		p99 := vals[len(vals)*99/100]
		var sum float64
		for _, v := range vals {
			sum += v
		}
		out = append(out, LatencySnapshot{
			Op:   key.(string),
			P50:  p50,
			P90:  p90,
			P99:  p99,
			Avg:  sum / float64(len(vals)),
			Hits: len(vals),
		})
		return true
	})
	return out
}

// Snapshot returns a point-in-time snapshot of all counters.
type Snapshot struct {
	Ops              map[string]uint64 `json:"ops"`
	Bytes            map[string]int64  `json:"bytes"`
	CacheHits        uint64            `json:"cacheHits"`
	CacheMisses      uint64            `json:"cacheMisses"`
	CacheRebuilds    uint64            `json:"cacheRebuilds"`
	Generation       uint64            `json:"generation"`
	ConfigReloads    uint64            `json:"configReloads"`
	WatcherEvents    uint64            `json:"watcherEvents"`
	WatcherOverflows uint64            `json:"watcherOverflows"`
	EventsDropped    uint64            `json:"eventsDropped"`
}

// Snapshot returns a point-in-time snapshot.
func (m *JanusMetrics) Snapshot() Snapshot {
	s := Snapshot{
		Ops:              make(map[string]uint64),
		Bytes:            make(map[string]int64),
		CacheHits:        m.CacheHit.Load(),
		CacheMisses:      m.CacheMiss.Load(),
		CacheRebuilds:    m.CacheRebuild.Load(),
		Generation:       m.Generation.Load(),
		ConfigReloads:    m.ConfigReloads.Load(),
		WatcherEvents:    m.WatcherEvents.Load(),
		WatcherOverflows: m.WatcherOverflows.Load(),
		EventsDropped:    m.EventsDropped.Load(),
	}
	m.OpsTotal.Range(func(key, val any) bool {
		s.Ops[key.(string)] = val.(*atomic.Uint64).Load()
		return true
	})
	m.BytesServed.Range(func(key, val any) bool {
		s.Bytes[key.(string)] = val.(*atomic.Int64).Load()
		return true
	})
	return s
}

func sortFloat64s(vals []float64) {
	// Simple insertion sort — the list is small (≤4096) and usually nearly
	// sorted by insertion time.
	for i := 1; i < len(vals); i++ {
		for j := i; j > 0 && vals[j] < vals[j-1]; j-- {
			vals[j], vals[j-1] = vals[j-1], vals[j]
		}
	}
}
