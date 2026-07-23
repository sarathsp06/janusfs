# Prometheus-only Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace JanusFS-owned metrics storage with direct Prometheus collector recording and update the dashboard/API to treat `/metrics` as the only metrics surface.

**Architecture:** `internal/obs` keeps the event vocabulary but exposes one primary `Recorder` that owns a Prometheus registry, cached metric handles, and optional non-blocking history fanout. Runtime wiring passes one recorder instead of separate `JanusMetrics`, `TopN`, `EventBus`, and custom collector types. The dashboard drops top/latency panels and links operators to `/metrics`.

**Tech Stack:** Go, `github.com/prometheus/client_golang/prometheus`, `promhttp`, stdlib `net/http`, embedded static HTML/JS.

## Global Constraints

- SPEC is binding; behavior must trace to FR/NFR numbers.
- NFR-5: FUSE handlers must never block on observability or history consumers.
- NFR-1: Events/logs carry paths and pattern names, never matched file content.
- No OpenTelemetry or new telemetry dependencies.
- No path labels in Prometheus metrics.
- No SQL outside `internal/history` Store methods.
- No logging outside `internal/logging.New(component)`.
- Keep transport layers thin: `internal/api` translates and delegates; it does not decide, redact, or query SQL directly.

---

## File structure

- Modify `internal/obs/event.go`: keep event vocabulary; optionally add small helper label constants if useful.
- Create `internal/obs/recorder.go`: Prometheus-native recorder, cached metric handles, optional non-blocking history fanout.
- Replace `internal/obs/obs_test.go`: recorder-focused tests.
- Delete `internal/obs/eventbus.go`: event bus is absorbed into recorder history fanout.
- Delete `internal/obs/metrics.go`: custom metrics store removed.
- Delete `internal/obs/topn.go`: live top-N removed.
- Delete `internal/obs/prometheus.go`: custom collector wrapper removed.
- Modify `internal/api/server.go`: accept a Prometheus registry, remove `/top` and `/latency`, simplify summary/status.
- Modify `internal/api/api_test.go`: update API construction and endpoint expectations.
- Modify `internal/vfsmeta/vfsmeta.go`: remove embedded op metrics from status JSON.
- Modify `cmd/janusfs/runtime.go`: replace metrics/event bus/top-N wiring with one recorder.
- Modify `cmd/janusfs/mock_dev.go`: same as runtime, with recorder-emitted sample events if needed.
- Modify `internal/ui/index.html`: remove top/latency panels and fetch loops; add `/metrics` card/link.

---

### Task 1: Replace `internal/obs` metrics store with Prometheus recorder

**Files:**
- Create: `internal/obs/recorder.go`
- Modify: `internal/obs/obs_test.go`
- Keep: `internal/obs/event.go`
- Delete after implementation compiles: `internal/obs/eventbus.go`, `internal/obs/metrics.go`, `internal/obs/topn.go`, `internal/obs/prometheus.go`

**Interfaces:**
- Consumes: existing `obs.Event`, `obs.Op`, `obs.Decision`, `obs.CacheResult` from `internal/obs/event.go`.
- Produces:
  - `type HistorySink interface { Record(Event) }`
  - `func NewRecorder(historySink HistorySink) *Recorder`
  - `func (r *Recorder) Emit(Event)`
  - `func (r *Recorder) SetGeneration(uint64)`
  - `func (r *Recorder) IncConfigReloads()`
  - `func (r *Recorder) Registry() *prometheus.Registry`
  - `func (r *Recorder) Close()`

- [ ] **Step 1: Replace `internal/obs/obs_test.go` with failing recorder tests**

Write this file content:

```go
package obs

import (
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

type blockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingSink) Record(Event) {
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.release
}

func TestRecorderEmitsPrometheusMetrics(t *testing.T) {
	r := newRecorder(nil, 0)
	defer r.Close()

	r.SetGeneration(7)
	r.IncConfigReloads()
	r.Emit(Event{Op: OpRead, Decision: Allowed, Bytes: 128, LatencyUs: 42, Cache: CacheHit})
	r.Emit(Event{Op: OpOpen, Decision: Hidden, LatencyUs: 3, Cache: CacheNA})

	assertMetric(t, r.Registry(), "janusfs_ops_total", map[string]string{"op": "read", "decision": "ALLOWED"}, 1)
	assertMetric(t, r.Registry(), "janusfs_ops_total", map[string]string{"op": "open", "decision": "HIDDEN"}, 1)
	assertMetric(t, r.Registry(), "janusfs_bytes_served_total", map[string]string{"decision": "ALLOWED"}, 128)
	assertMetric(t, r.Registry(), "janusfs_cache_results_total", map[string]string{"result": "hit"}, 1)
	assertMetric(t, r.Registry(), "janusfs_generation", nil, 7)
	assertMetric(t, r.Registry(), "janusfs_config_reloads_total", nil, 1)
}

func TestRecorderHistoryFanoutDropsInsteadOfBlocking(t *testing.T) {
	sink := newBlockingSink()
	r := newRecorder(sink, 1)
	defer r.Close()

	r.Emit(Event{Op: OpRead, Decision: Allowed})
	<-sink.entered

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Emit(Event{Op: OpRead, Decision: Allowed})
		}()
	}
	wg.Wait()

	assertMetric(t, r.Registry(), "janusfs_events_dropped_total", nil, 1)
	close(sink.release)
}

func TestRecorderDoesNotUsePathLabels(t *testing.T) {
	r := newRecorder(nil, 0)
	defer r.Close()

	r.Emit(Event{Op: OpRead, Decision: Allowed, Path: "/secret/path.env", Bytes: 1})

	families, err := r.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "path" || strings.Contains(lp.GetValue(), "secret/path.env") {
					t.Fatalf("metric %s leaked path label %q=%q", mf.GetName(), lp.GetName(), lp.GetValue())
				}
			}
		}
	}
}

func assertMetric(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string, want float64) {
	t.Helper()
	got := metricValue(t, reg, name, labels)
	if got != want {
		t.Fatalf("%s%v = %v, want %v", name, labels, got, want)
	}
}

func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.Metric {
			if !labelsMatch(m, labels) {
				continue
			}
			if m.Counter != nil {
				return m.Counter.GetValue()
			}
			if m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(m *io_prometheus_client.Metric, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		found := false
		for _, lp := range m.Label {
			if lp.GetName() == k && lp.GetValue() == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/obs
```

Expected: FAIL because `newRecorder`, `Recorder`, and related methods are not defined.

- [ ] **Step 3: Implement `internal/obs/recorder.go`**

Write this file:

```go
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
// and optionally fans events out to history without blocking FUSE handlers
// (NFR-5).
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

func (r *Recorder) Registry() *prometheus.Registry {
	return r.registry
}

func (r *Recorder) SetGeneration(gen uint64) {
	r.generation.Set(float64(gen))
}

func (r *Recorder) IncConfigReloads() {
	r.configReloads.Inc()
}

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

func (r *Recorder) Close() {
	r.closeOnce.Do(func() {
		if r.historyCh != nil {
			close(r.historyCh)
		}
		<-r.done
	})
}
```

- [ ] **Step 4: Run obs tests and fix compile errors**

Run:

```bash
go test ./internal/obs
```

Expected: PASS.

- [ ] **Step 5: Delete obsolete obs files**

Run:

```bash
rm internal/obs/eventbus.go internal/obs/metrics.go internal/obs/topn.go internal/obs/prometheus.go
```

- [ ] **Step 6: Run the expected compile failures for callers**

Run:

```bash
go test ./...
```

Expected: FAIL in `cmd/janusfs`, `internal/api`, and tests referencing deleted `JanusMetrics`, `TopN`, `EventBus`, or `NewCollector`. These are fixed in later tasks.

- [ ] **Step 7: Commit Task 1**

```bash
git add internal/obs
git commit -m "refactor(obs): record metrics directly in prometheus"
```

---

### Task 2: Simplify API server metrics surface

**Files:**
- Modify: `internal/api/server.go`
- Modify: `internal/api/api_test.go`
- Modify: `internal/vfsmeta/vfsmeta.go`

**Interfaces:**
- Consumes: `*prometheus.Registry` from `obs.Recorder.Registry()`.
- Produces:
  - `func New(uiFS fs.FS, token string, reg *prometheus.Registry, hist *history.Store) *Server`
  - `func (s *Server) SetVFSMeta(root string, providerStats func() (int, int64, uint64, uint64, uint64), resolvePath func(relPath string, isDir bool) (string, []string, string), watcherAlive func() bool, generation func() uint64)`

- [ ] **Step 1: Update API tests to expect new surface**

In `internal/api/api_test.go`, replace the `testMetrics` helper with:

```go
func testRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "janusfs_test_metric", Help: "test metric"}))
	return reg
}
```

Update imports to include:

```go
"github.com/prometheus/client_golang/prometheus"
```

Replace `testServer` with:

```go
func testServer() *Server {
	return New(nil, "test-token", testRegistry(), nil)
}
```

Replace `TestSummaryEndpoint` with:

```go
func TestSummaryEndpoint(t *testing.T) {
	s := testServer()
	s.SetVFSMeta("/src/project", func() (int, int64, uint64, uint64, uint64) {
		return 2, 4096, 3, 4, 5
	}, nil, nil, func() uint64 { return 9 })

	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Status struct {
			Generation uint64 `json:"generation"`
			Cache struct {
				Entries  int   `json:"entries"`
				Bytes    int64 `json:"bytes"`
				Hits     uint64 `json:"hits"`
				Misses   uint64 `json:"misses"`
				Rebuilds uint64 `json:"rebuilds"`
			} `json:"cache"`
		} `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status.Generation != 9 {
		t.Fatalf("generation = %d, want 9", resp.Status.Generation)
	}
	if resp.Status.Cache.Entries != 2 || resp.Status.Cache.Hits != 3 || resp.Status.Cache.Misses != 4 || resp.Status.Cache.Rebuilds != 5 {
		t.Fatalf("cache status = %+v", resp.Status.Cache)
	}
}
```

Replace `TestTopEndpoint` with:

```go
func TestTopEndpointRemoved(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/top", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for removed top endpoint, got %d", w.Code)
	}
}
```

Replace `TestLatencyEndpoint` with:

```go
func TestLatencyEndpointRemoved(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/latency", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for removed latency endpoint, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run API tests to verify they fail**

Run:

```bash
go test ./internal/api
```

Expected: FAIL because `api.New` and `SetVFSMeta` signatures still use old metrics/top/bus types.

- [ ] **Step 3: Modify `internal/api/server.go` imports and fields**

Remove `obs` import unless still needed elsewhere. Remove fields:

```go
metrics *obs.JanusMetrics
promReg *prometheus.Registry
topN    *obs.TopN
bus     *obs.EventBus
```

Add fields:

```go
promReg    *prometheus.Registry
generation func() uint64
```

Change `New` to:

```go
func New(uiFS fs.FS, token string, reg *prometheus.Registry, hist *history.Store) *Server {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	s := &Server{
		mux:     http.NewServeMux(),
		promReg: reg,
		history: hist,
		token:   token,
		ui:      uiFS,
	}
	s.register()
	return s
}
```

- [ ] **Step 4: Remove deleted endpoint registration and handlers**

In `register`, delete:

```go
s.mux.HandleFunc("/api/v1/top", s.withToken(s.handleTop))
s.mux.HandleFunc("/api/v1/latency", s.withToken(s.handleLatency))
```

Delete functions:

```go
func (s *Server) handleTop(...)
func (s *Server) handleLatency(...)
```

- [ ] **Step 5: Update `SetVFSMeta` and summary**

Change `SetVFSMeta` signature to:

```go
func (s *Server) SetVFSMeta(root string, providerStats func() (int, int64, uint64, uint64, uint64), resolvePath func(relPath string, isDir bool) (string, []string, string), watcherAlive func() bool, generation func() uint64) {
	s.root = root
	s.providerStats = providerStats
	s.resolvePath = resolvePath
	s.watcherAlive = watcherAlive
	s.generation = generation
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
}
```

Replace `handleSummary` with:

```go
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	var entries int
	var bytes int64
	var hits, misses, rebuilds uint64
	if s.providerStats != nil {
		entries, bytes, hits, misses, rebuilds = s.providerStats()
	}
	var gen uint64
	if s.generation != nil {
		gen = s.generation()
	}
	writeJSON(w, map[string]any{
		"mount": map[string]any{
			"source":     s.root,
			"mountpoint": s.mountpoint,
		},
		"status": map[string]any{
			"generation": gen,
			"uptime":     int64(time.Since(s.startTime).Seconds()),
			"cache": map[string]any{
				"entries":  entries,
				"bytes":    bytes,
				"hits":     hits,
				"misses":   misses,
				"rebuilds": rebuilds,
			},
		},
	})
}
```

- [ ] **Step 6: Update status JSON handling**

In `internal/vfsmeta/vfsmeta.go`, remove the `Metrics map[string]uint64` field from `Status` and change `StatusJSON` signature to:

```go
func StatusJSON(startTime time.Time, gen uint64, watcherAlive bool, cacheEntries int, cacheBytes int64, cacheHits, cacheMisses, cacheRebuilds uint64) []byte
```

Inside `StatusJSON`, remove the `opCounts` defaulting block and set `Status` without `Metrics` and without `ConfigReloads` if this field is removed. Keep `ConfigReloads` only if the implementation chooses to pass a non-metrics operational counter; otherwise delete the struct field too.

In `internal/api/server.go`, update `handleStatusJSON` to use the generation callback and provider stats:

```go
gen := uint64(0)
if s.generation != nil {
	gen = s.generation()
}
b := vfsmeta.StatusJSON(start, gen, watcherAlive, entries, bytes, hits, misses, rebuilds)
```

- [ ] **Step 7: Run API tests**

Run:

```bash
go test ./internal/api ./internal/vfsmeta
```

Expected: PASS after import cleanup.

- [ ] **Step 8: Commit Task 2**

```bash
git add internal/api internal/vfsmeta
git commit -m "refactor(api): make prometheus metrics authoritative"
```

---

### Task 3: Rewire runtime and mock mounts to one recorder

**Files:**
- Modify: `cmd/janusfs/runtime.go`
- Modify: `cmd/janusfs/mock_dev.go`
- Modify if compile requires: `cmd/janusfs/daemon_test.go`

**Interfaces:**
- Consumes: `obs.NewRecorder`, `(*obs.Recorder).Emit`, `(*obs.Recorder).Registry`, `(*obs.Recorder).SetGeneration`, `(*obs.Recorder).IncConfigReloads`.
- Produces: working daemon/runtime wiring with no `JanusMetrics`, `TopN`, `EventBus`, or `drainEvents`.

- [ ] **Step 1: Update `mountRuntime` fields**

In `cmd/janusfs/runtime.go`, replace:

```go
metrics  *obs.JanusMetrics
eventBus *obs.EventBus
```

with:

```go
recorder *obs.Recorder
```

- [ ] **Step 2: Update `reload`**

Replace metrics generation storage with recorder calls:

```go
if rt.recorder != nil {
	rt.recorder.SetGeneration(rt.eng.Generation())
	rt.recorder.IncConfigReloads()
}
```

Keep provider invalidation unchanged.

- [ ] **Step 3: Update `startMount` construction**

Delete construction of `eventBus`, `metrics`, and `topN`.

After history setup, add:

```go
recorder := obs.NewRecorder(rt.hist)
recorder.SetGeneration(eng.Generation())
rt.recorder = recorder
```

Because `rt` is currently built before history setup, either:

1. create `rt` first with no recorder, set up history, then assign recorder; or
2. open history before building `rt`.

Use option 1 to minimize diff.

- [ ] **Step 4: Update API construction**

Replace:

```go
apiSrv := api.New(ui.FS, bearerToken, metrics, topN, eventBus, rt.hist)
```

with:

```go
apiSrv := api.New(ui.FS, bearerToken, recorder.Registry(), rt.hist)
```

Update `SetVFSMeta` call to include generation callback:

```go
apiSrv.SetVFSMeta(cfg.Src, func() (int, int64, uint64, uint64, uint64) {
	ps := prov.Stats()
	return ps.Entries, ps.Bytes, ps.Hits, ps.Misses, ps.Rebuilds
}, func(relPath string, isDir bool) (string, []string, string) {
	res := eng.Resolve(relPath, isDir)
	return res.Decision.String(), res.PatternNames, res.RuleRef
}, func() bool { return true }, eng.Generation)
```

- [ ] **Step 5: Replace observer wiring**

Replace:

```go
Observe: makeObserver(eventBus),
```

with:

```go
Observe: makeObserver(recorder),
```

Change `makeObserver` signature to:

```go
func makeObserver(recorder *obs.Recorder) func(mount.OpEvent)
```

Inside it, replace `bus.Emit(...)` with `recorder.Emit(...)`.

- [ ] **Step 6: Delete `drainEvents`**

Remove the entire `drainEvents` function from `cmd/janusfs/runtime.go`.

- [ ] **Step 7: Update shutdown**

In `stop`, replace event bus close with recorder close:

```go
if rt.recorder != nil {
	rt.recorder.Close()
}
```

Keep history close after recorder close so queued history events flush before DB close:

```go
if rt.recorder != nil { rt.recorder.Close() }
if rt.hist != nil { rt.hist.Close() }
```

- [ ] **Step 8: Update mock runtime**

In `cmd/janusfs/mock_dev.go`, delete `eventBus`, `metrics`, and `topN` construction.

After `rt` and optional history setup, create:

```go
recorder := obs.NewRecorder(nil)
recorder.SetGeneration(eng.Generation())
rt.recorder = recorder
```

Emit sample events instead of manually recording metrics/topN:

```go
recorder.Emit(obs.Event{Op: obs.OpRead, Path: "README.md", Decision: obs.Allowed, Bytes: 3300, LatencyUs: 25, Cache: obs.CacheNA})
recorder.Emit(obs.Event{Op: obs.OpRead, Path: "app.env", Decision: obs.Masked, Bytes: 1200, LatencyUs: 80, Cache: obs.CacheHit})
recorder.Emit(obs.Event{Op: obs.OpOpen, Path: "db.secret", Decision: obs.Hidden, LatencyUs: 10, Cache: obs.CacheNA})
```

Construct API with:

```go
apiSrv := api.New(ui.FS, bearerToken, recorder.Registry(), nil)
```

Update `SetVFSMeta(..., eng.Generation)`.

- [ ] **Step 9: Run command package tests**

Run:

```bash
go test ./cmd/janusfs
```

Expected: PASS after compile fixes.

- [ ] **Step 10: Commit Task 3**

```bash
git add cmd/janusfs
git commit -m "refactor(cmd): wire mounts through obs recorder"
```

---

### Task 4: Update dashboard to remove stored-metrics panels

**Files:**
- Modify: `internal/ui/index.html`

**Interfaces:**
- Consumes: new `/api/v1/summary` response shape with `status.generation` and `status.cache`.
- Produces: dashboard with no calls to `/api/v1/top` or `/api/v1/latency`; visible `/metrics` link.

- [ ] **Step 1: Remove Top Touched Paths and Latency HTML panels**

In `internal/ui/index.html`, delete the whole panel block containing:

```html
<h2>Top Touched Paths</h2>
```

and delete the whole panel block containing:

```html
<h2>Latency</h2>
```

Keep the Live Events panel and Operator Tips panel. A compact replacement layout is:

```html
  <div style="margin-top:var(--sp-xl)">
    <div class="grid-2">
      <div class="panel">
        <h2>Live Events</h2>
        <div class="events-feed" id="events-feed"><div class="empty-state">Waiting for filesystem activity through the mountpoint</div></div>
      </div>
      <div class="panel">
        <h2>Metrics</h2>
        <div class="notice">Prometheus metrics are exposed at <a href="/metrics" target="_blank" rel="noreferrer">/metrics</a>. Use Prometheus/PromQL for rates and latency queries.</div>
      </div>
    </div>
  </div>

  <div style="margin-top:var(--sp-xl)">
    <div class="panel">
      <h2>Operator Tips</h2>
      <div class="notice">Use the mountpoint path above for agents. Edit rules below, then click <strong>Reload rules</strong>; real source files are never modified by reads through the mount.</div>
    </div>
  </div>
```

- [ ] **Step 2: Change stat-card labels away from metrics counts**

Replace the first three stat cards with operational status cards:

```html
    <div class="stat-card">
      <h3>Generation</h3>
      <div class="value" id="stat-generation">—</div>
      <div class="sub">active rule snapshot</div>
    </div>
    <div class="stat-card">
      <h3>Cache Entries</h3>
      <div class="value" id="stat-cache-entries">—</div>
      <div class="sub">redacted files cached in RAM</div>
    </div>
    <div class="stat-card">
      <h3>Cache Bytes</h3>
      <div class="value" id="stat-cache-bytes">—</div>
      <div class="sub">current RAM cache footprint</div>
    </div>
```

Keep the existing cache hit/miss/rebuild card or rename it:

```html
    <div class="stat-card">
      <h3>Cache Results</h3>
      <div class="value" id="stat-cache">—</div>
      <div class="sub">hit / miss / rebuild</div>
    </div>
```

- [ ] **Step 3: Update summary rendering JavaScript**

Replace:

```js
.then(function(data) { renderSummary(data.snapshot, data.mount || {}); })
```

with:

```js
.then(function(data) { renderSummary(data.status || {}, data.mount || {}); })
```

Replace `renderSummary` body with:

```js
  function renderSummary(status, mount) {
    var cache = status.cache || {};
    document.getElementById('stat-generation').textContent = status.generation || '—';
    document.getElementById('stat-cache-entries').textContent = fmtNum(cache.entries || 0);
    document.getElementById('stat-cache-bytes').textContent = fmtBytes(cache.bytes || 0);
    document.getElementById('stat-cache').textContent = fmtNum(cache.hits || 0) + ' / ' + fmtNum(cache.misses || 0) + ' / ' + fmtNum(cache.rebuilds || 0);
    document.getElementById('generation-label').textContent = 'gen ' + (status.generation || '—');
    var source = mount.source || '—';
    var mountpoint = mount.mountpoint || '—';
    document.getElementById('overview-source').textContent = source;
    document.getElementById('overview-mountpoint').textContent = mountpoint;
    document.getElementById('overview-cd').textContent = mountpoint === '—' ? 'cd "..."' : 'cd "' + mountpoint + '"';
    document.getElementById('overview-refresh').textContent = 'last refresh just now';
    document.getElementById('mount-label').textContent = mountpoint !== '—' ? shortPath(mountpoint) : window.location.host;
    setStatus('live');
  }
```

- [ ] **Step 4: Remove top/latency JavaScript**

Delete functions:

```js
window.switchTopTab = function(tab) { ... };
function fetchTop() { ... }
function renderTop(id, entries, mode) { ... }
function fetchLatency() { ... }
function renderLatency(ops) { ... }
```

Delete startup and interval calls:

```js
fetchTop();
fetchLatency();
setInterval(fetchTop, POLL);
setInterval(fetchLatency, POLL);
```

In `reloadRules`, delete:

```js
fetchTop();
```

- [ ] **Step 5: Add dashboard static checks**

Run:

```bash
rg "/api/v1/top|/api/v1/latency|fetchTop|fetchLatency|switchTopTab|top-reads|top-bytes|latency-panel|stat-allowed|stat-masked|stat-hidden" internal/ui/index.html
```

Expected: no output.

- [ ] **Step 6: Commit Task 4**

```bash
git add internal/ui/index.html
git commit -m "refactor(ui): remove stored metrics panels"
```

---

### Task 5: Final compile cleanup and verification

**Files:**
- Modify any remaining compile failures from old observability symbols.
- Check: `go.mod`, `go.sum` only if imports require tidy changes.

**Interfaces:**
- Consumes: all previous tasks.
- Produces: repo with no custom metrics store references and passing tests.

- [ ] **Step 1: Search for deleted symbols**

Run:

```bash
rg "JanusMetrics|NewTopN|TopN|EventBus|NewEventBus|NewCollector|RecordOp|RecordBytes|RecordLatency|LatencySnapshots|Snapshot\(|drainEvents|fetchTop|fetchLatency|/api/v1/top|/api/v1/latency" cmd internal
```

Expected: no output, except references in docs/specs/plans are okay if the command is scoped to `cmd internal`.

- [ ] **Step 2: Run gofmt**

Run:

```bash
gofmt -w internal/obs internal/api internal/vfsmeta cmd/janusfs
```

Expected: no output.

- [ ] **Step 3: Run full tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Verify `/metrics` handler through API test manually if needed**

If no existing test covers `/metrics`, add this to `internal/api/api_test.go`:

```go
func TestMetricsEndpoint(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "janusfs_test_metric", Help: "test metric"})
	reg.MustRegister(g)
	g.Set(12)
	s := New(nil, "test-token", reg, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "janusfs_test_metric 12") {
		t.Fatalf("metrics body missing test metric: %s", w.Body.String())
	}
}
```

Ensure `strings` is already imported; it is currently used by reveal tests.

Run:

```bash
go test ./internal/api -run TestMetricsEndpoint -v
```

Expected: PASS.

- [ ] **Step 5: Run package tests most affected by runtime wiring**

Run:

```bash
go test ./internal/obs ./internal/api ./cmd/janusfs -v
```

Expected: PASS.

- [ ] **Step 6: Commit final cleanup**

```bash
git add .
git commit -m "test: verify prometheus-only observability"
```

If there are no changes after verification, skip this commit.

---

## Self-review checklist

- Spec coverage: Tasks remove custom metrics store, custom collector, top/latency endpoints, dashboard panels, and runtime multi-object wiring. Tasks add Prometheus-native recorder with cached handles and non-blocking history fanout.
- Performance: Task 1 caches metric handles and avoids per-event snapshot/top/latency storage. No path labels are introduced.
- Dependency direction: `internal/obs` does not import `internal/history` or `internal/mount`; it uses `HistorySink` and runtime-local mount event adaptation.
- Security: No file content is recorded; path appears in events/history only as before; path is explicitly not a Prometheus label.
- Verification: Plan ends with symbol search, gofmt, and `go test ./...`.
