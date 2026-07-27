package obs

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const defaultHistoryBuffer = 4096

// HistorySink receives observability events for persistence. history.Store
// satisfies this without internal/obs importing internal/history.
type HistorySink interface {
	Record(Event)
}

type opDecision struct {
	op       Op
	decision Decision
}

// Recorder records JanusFS events directly into native Prometheus collectors
// and optionally fans events out to history without blocking FUSE handlers.
type Recorder struct {
	registry *prometheus.Registry

	opsTotal       *prometheus.CounterVec
	bytesServed    *prometheus.CounterVec
	cacheResults   *prometheus.CounterVec
	handlerLatency *prometheus.HistogramVec
	generation     prometheus.Gauge
	configReloads  prometheus.Counter
	eventsDropped  prometheus.Counter

	ops     map[opDecision]prometheus.Counter
	bytes   map[Decision]prometheus.Counter
	cache   map[CacheResult]prometheus.Counter
	latency map[Op]prometheus.Observer

	historySink HistorySink
	historyCh   chan Event
	closeOnce   sync.Once
	done        chan struct{}
}

// NewRecorder creates a Prometheus-backed event recorder.
func NewRecorder(historySink HistorySink) *Recorder {
	return newRecorder(historySink, defaultHistoryBuffer)
}

func newRecorder(historySink HistorySink, historyBuffer int) *Recorder {
	r := &Recorder{
		registry: prometheus.NewRegistry(),
		ops:      make(map[opDecision]prometheus.Counter),
		bytes:    make(map[Decision]prometheus.Counter),
		cache:    make(map[CacheResult]prometheus.Counter),
		latency:  make(map[Op]prometheus.Observer),
		done:     make(chan struct{}),
	}

	r.opsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "janusfs_ops_total",
		Help: "Total filesystem operations.",
	}, []string{"op", "decision"})
	r.bytesServed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "janusfs_bytes_served_total",
		Help: "Total bytes served by decision.",
	}, []string{"decision"})
	r.cacheResults = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "janusfs_cache_results_total",
		Help: "Total cache results.",
	}, []string{"result"})
	r.handlerLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "janusfs_handler_latency_microseconds",
		Help:    "Filesystem handler latency in microseconds.",
		Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 50000, 100000, 500000, 1000000},
	}, []string{"op"})
	r.generation = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "janusfs_generation",
		Help: "Current rules generation.",
	})
	r.configReloads = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "janusfs_config_reloads_total",
		Help: "Total config reloads.",
	})
	r.eventsDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "janusfs_events_dropped_total",
		Help: "Total observability/history events dropped because the non-blocking queue was full.",
	})

	r.registry.MustRegister(r.opsTotal, r.bytesServed, r.cacheResults, r.handlerLatency, r.generation, r.configReloads, r.eventsDropped)
	r.cacheMetricHandles()

	if historySink != nil && historyBuffer > 0 {
		r.historySink = historySink
		r.historyCh = make(chan Event, historyBuffer)
		go r.drainHistory()
	} else {
		close(r.done)
	}
	return r
}

func (r *Recorder) cacheMetricHandles() {
	for _, op := range knownOps() {
		r.latency[op] = r.handlerLatency.WithLabelValues(string(op))
		for _, d := range knownDecisions() {
			r.ops[opDecision{op: op, decision: d}] = r.opsTotal.WithLabelValues(string(op), d.String())
		}
	}
	for _, d := range knownDecisions() {
		r.bytes[d] = r.bytesServed.WithLabelValues(d.String())
	}
	for _, c := range []CacheResult{CacheHit, CacheMiss, CacheRebuild} {
		r.cache[c] = r.cacheResults.WithLabelValues(string(c))
	}
}

func knownDecisions() []Decision {
	return []Decision{Allowed, Masked, Hidden}
}

func knownOps() []Op {
	return []Op{OpLookup, OpGetattr, OpOpen, OpRead, OpReaddir, OpWrite, OpCreate, OpUnlink, OpMkdir, OpRmdir, OpRename, OpSymlink, OpReadlink, OpSetattr, OpGetxattr}
}

// Registry returns the Prometheus registry backing /metrics.
func (r *Recorder) Registry() *prometheus.Registry {
	return r.registry
}

// SetGeneration sets the current rules generation gauge.
func (r *Recorder) SetGeneration(gen uint64) {
	r.generation.Set(float64(gen))
}

// IncConfigReloads increments the config reload counter.
func (r *Recorder) IncConfigReloads() {
	r.configReloads.Inc()
}

// Emit records e into Prometheus and optionally queues it for history.
func (r *Recorder) Emit(e Event) {
	if c := r.ops[opDecision{op: e.Op, decision: e.Decision}]; c != nil {
		c.Inc()
	} else if e.Decision.String() != "UNKNOWN" {
		r.opsTotal.WithLabelValues(string(e.Op), e.Decision.String()).Inc()
	}

	if e.Bytes > 0 {
		if c := r.bytes[e.Decision]; c != nil {
			c.Add(float64(e.Bytes))
		}
	}
	if e.Cache != "" && e.Cache != CacheNA {
		if c := r.cache[e.Cache]; c != nil {
			c.Inc()
		}
	}
	if e.LatencyUs > 0 {
		if h := r.latency[e.Op]; h != nil {
			h.Observe(float64(e.LatencyUs))
		} else {
			r.handlerLatency.WithLabelValues(string(e.Op)).Observe(float64(e.LatencyUs))
		}
	}

	if r.historyCh != nil {
		select {
		case r.historyCh <- e:
		default:
			r.eventsDropped.Inc()
		}
	}
}

func (r *Recorder) drainHistory() {
	defer close(r.done)
	for e := range r.historyCh {
		r.historySink.Record(e)
	}
}

// Close stops history fanout after queued events are drained.
func (r *Recorder) Close() {
	r.closeOnce.Do(func() {
		if r.historyCh != nil {
			close(r.historyCh)
		}
		<-r.done
	})
}
