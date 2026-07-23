# Prometheus-only observability design

Date: 2026-07-23

## Goal

Simplify `internal/obs` by removing JanusFS-owned metrics storage. JanusFS records metrics directly into Prometheus client collectors and exposes them through `/metrics`. The dashboard no longer reads stored metrics snapshots, top-N lists, or custom latency summaries.

This change improves module depth by making `internal/obs` the single event-recording seam while deleting shallow helper types that callers currently coordinate manually.

## Current problem

`internal/obs` currently exposes separate shallow types:

- `JanusMetrics` stores atomic counters and custom latency samples.
- `TopN` stores per-path read/byte counts.
- `EventBus` provides non-blocking event intake.
- `Collector` adapts `JanusMetrics` back into Prometheus exposition.

`cmd/janusfs/runtime.go` manually constructs all of them and runs `drainEvents` to coordinate updates. `internal/api/server.go` holds separate references to metrics, top-N, event bus, Prometheus registry, provider stats callbacks, and custom endpoint handlers.

The result is two metrics systems: JanusFS stores metrics in Go structs, then Prometheus reads those structs. That is unnecessary.

## Decision

Prometheus `/metrics` is the only metrics surface.

JanusFS will not maintain a custom metrics store for dashboard/API snapshots. It will record metrics directly into native Prometheus collectors.

Delete or stop using:

- custom `JanusMetrics` snapshot store,
- custom Prometheus `Collector` wrapper over that store,
- custom latency snapshot arrays,
- live top-N metrics endpoints and dashboard panels.

Keep only a small Prometheus-backed recorder that is safe to call from FUSE handlers.

## New `internal/obs` module shape

`internal/obs` keeps the event vocabulary:

- `Event`
- `Op`
- `Decision`
- `CacheResult`

Add one primary type:

```go
type Recorder struct {
    // owns a prometheus.Registry and native collectors
}
```

Primary methods:

```go
func NewRecorder(historySink HistorySink) *Recorder
func (r *Recorder) Emit(Event)
func (r *Recorder) SetGeneration(uint64)
func (r *Recorder) IncConfigReloads()
func (r *Recorder) Registry() *prometheus.Registry
func (r *Recorder) Close()
```

`HistorySink` is a tiny interface to avoid a package cycle:

```go
type HistorySink interface {
    Record(Event)
}
```

`history.Store` already satisfies this interface.

## Prometheus collectors

The recorder owns a fresh per-mount `prometheus.Registry` and registers native collectors such as:

- `janusfs_ops_total{op,decision}` counter
- `janusfs_bytes_served_total{decision}` counter
- `janusfs_cache_results_total{result}` counter
- `janusfs_handler_latency_microseconds{op}` histogram
- `janusfs_generation` gauge
- `janusfs_config_reloads_total` counter
- `janusfs_events_dropped_total` counter

Allowed labels are low-cardinality only:

- `op`
- `decision`
- `result`

Paths must not be used as Prometheus labels.

## Hot-path performance design

`Emit` is called from FUSE operation paths, so it must stay cheap and non-blocking.

The recorder should cache Prometheus child handles at construction time instead of calling `WithLabelValues` for every event. For example, precompute counters keyed by `(op, decision)`, cache-result counters keyed by `CacheResult`, byte counters keyed by `Decision`, and latency observers keyed by `Op`.

Hot path shape:

```go
func (r *Recorder) Emit(e Event) {
    r.recordPrometheus(e) // direct counter/histogram updates

    if r.historyCh != nil {
        select {
        case r.historyCh <- e:
        default:
            r.eventsDropped.Inc()
        }
    }
}
```

Metrics are recorded synchronously into Prometheus collectors. History forwarding remains asynchronous and non-blocking, preserving NFR-5: FUSE handlers must never block on history or observability consumers.

Avoid per-event allocations:

- no snapshot maps,
- no top-N maps,
- no latency slices,
- no sorting on dashboard requests,
- no path labels.

Using `Decision.String()` is acceptable because it returns constants and does not allocate; the important optimization is cached Prometheus metric handles.

## History fanout

If history is enabled, `Recorder` owns a bounded channel and a goroutine that forwards events to `HistorySink.Record`.

If the channel is full, drop the history event and increment `janusfs_events_dropped_total`. Dropping history/observability events must not affect mount behavior.

If history is disabled, no channel or goroutine is needed.

## API and dashboard changes

`/metrics` remains and is backed directly by `Recorder.Registry()`.

Delete these metric-dashboard endpoints:

- `GET /api/v1/top`
- `GET /api/v1/latency`

Update the dashboard to remove:

- Top Files panel,
- Latency panel,
- JS fetches for `/api/v1/top`,
- JS fetches for `/api/v1/latency`.

Add a simple dashboard card or link stating that Prometheus metrics are available at `/metrics`.

Keep `/api/v1/summary`, but make it non-authoritative for metrics. It should return only live mount/status information needed by the dashboard, such as:

- source,
- mountpoint,
- uptime,
- generation from an explicit callback or recorder gauge mirror if needed,
- provider cache stats if still useful for status.

Do not gather and parse Prometheus metrics to recreate the old metrics snapshot.

## Runtime wiring

`mountRuntime` should replace separate observability fields:

```go
metrics  *obs.JanusMetrics
eventBus *obs.EventBus
```

with:

```go
recorder *obs.Recorder
```

`startMount` should construct the recorder after history setup:

```go
rec := obs.NewRecorder(rt.hist)
rec.SetGeneration(eng.Generation())
```

The FUSE observer adapter remains outside `internal/obs` to avoid importing `internal/mount` into `internal/obs`.

`api.New` should take the Prometheus registry instead of `JanusMetrics`, `TopN`, and `EventBus`.

## VFS status changes

`.janusfs/status.json` should stop embedding op-count metrics. It may keep operational state such as:

- uptime,
- generation,
- config reload count if tracked,
- cache entries/bytes/hits/misses/rebuilds from provider stats,
- Go version/version.

Prometheus `/metrics` is the metrics interface.

## Test plan

Update tests to verify:

1. `Recorder.Emit` increments Prometheus counters visible through `Registry().Gather()` or `/metrics`.
2. History forwarding is non-blocking and dropped events increment the Prometheus dropped counter.
3. `/api/v1/top` and `/api/v1/latency` are removed.
4. Dashboard HTML no longer references deleted endpoints.
5. `/metrics` still exposes JanusFS metrics.
6. Existing API, daemon, provider, history, and mount-adjacent tests pass.

Run:

```sh
go test ./...
```

## Non-goals

- No embedded PromQL engine.
- No path-labeled Prometheus metrics.
- No custom metrics snapshot store.
- No live top-N replacement in this pass.
- No OpenTelemetry or new telemetry dependencies.

## Expected impact

Positive performance impact from deleting duplicate storage and dashboard polling work:

- removes custom counter maps,
- removes custom latency samples and sorting,
- removes top-N mutex/map/sort work,
- removes Prometheus wrapper collector over custom counters,
- removes JSON snapshot conversions for top/latency endpoints.

The remaining hot-path cost is native Prometheus counter/histogram updates plus optional non-blocking history enqueue. This is acceptable for this phase and simpler than maintaining two metrics systems.
