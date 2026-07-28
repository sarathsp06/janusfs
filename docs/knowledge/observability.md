---
type: Subsystem
title: Observability
description: The event path from a FUSE handler to Prometheus metrics, SQLite rollups, and the dashboard API.
tags: [obs, metrics, history, api, sqlite]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: event
    resource: /internal/obs/event.go
    title: Event, Op, Decision, CacheResult vocabularies
  - id: recorder
    resource: /internal/obs/recorder.go
    title: Recorder, metric handles, history fan-out
  - id: history
    resource: /internal/history/store.go
    title: Store, batched writer, schema, pruning
  - id: api
    resource: /internal/api/server.go
    title: HTTP routes
  - id: runtime
    resource: /cmd/janusfs/runtime.go
    title: makeObserver adapter
---

# The event path

```
JanusNode.observe()          mount.OpEvent  (adapter vocabulary)
      │                      internal/mount/janus_node.go:90
      ▼
makeObserver()               translate to obs.Event
      │                      cmd/janusfs/runtime.go:211
      ▼
obs.Recorder.Emit()          ├─▶ prometheus counters/histograms (synchronous)
                             └─▶ buffered channel ─▶ history.Store (async)
```

There are two vocabularies on purpose. `mount.OpEvent`
(`internal/mount/mount_darwin.go:29`) uses plain strings so
`internal/mount` needs no dependency on `internal/obs`. `makeObserver`
(`cmd/janusfs/runtime.go:211`) translates: `"ALLOWED"`/`"MASKED"`/`"HIDDEN"`
map to the `obs.Decision` enum, while `"PANIC"` and `"CONFIG_READONLY"` both map
to `obs.Hidden` **plus a synthetic error**, so they are counted as denials and
still distinguishable in the error field. An unrecognised decision string is
dropped silently.

`obs.Decision` is deliberately a mirror of `engine.Decision` rather than an
import (`internal/obs/event.go:14`), keeping `obs` free of any dependency on the
policy engine.

`obs.Op` enumerates the operation names as typed constants
(`event.go:37`) and `CacheResult` records how a read was served — hit, miss,
rebuild, or `na`.

# Non-blocking by construction

The rule is that observability never blocks the data path. `observe()` is called
synchronously from the FUSE handler and must not block, so the fan-out to history
goes through a buffered channel drained by a dedicated goroutine
(`Recorder.Emit`, `internal/obs/recorder.go:144`); a full buffer drops the event
rather than stalling the handler.

The same principle applies one layer down: `history.Store.Record`
(`internal/history/store.go:113`) hands off to a `batchedWriter` goroutine
(`:139`) that flushes transactionally. A slow or failed flush drops and counts;
it never reaches back into a FUSE handler.

# Metrics

`Recorder` owns a `*prometheus.Registry` (`recorder.go:129`), and metric handles
are pre-resolved for every (op, decision) pair at construction —
`cacheMetricHandles` (`:105`) over `knownOps()` × `knownDecisions()` — so the
hot path does no label lookup or map allocation per event.

Two counters are driven from outside the event stream:
`SetGeneration(gen)` (`:134`) and `IncConfigReloads()` (`:139`), both called by
`mountRuntime.reload()` (`cmd/janusfs/runtime.go:53`).

# History store

One SQLite database per mount at
`~/.janusfs/history/<basename>-<hash12>.db`, opened via `modernc.org/sqlite`
(pure Go, no cgo) through `jmoiron/sqlx`.

`migrate` (`store.go:304`) creates four tables:

| Table | Holds |
|---|---|
| `schema_version` | migration bookkeeping |
| `sessions` | one row per mount lifetime, opened by `insertSessionStart` (`:268`), closed by `endSession` (`:276`) |
| `op_rollups` | aggregate buckets, never raw events |
| `coverage` | point-in-time snapshots of what is masked and hidden |

The contents are **rollups and path names only, never file content**. That is a
deliberate threat-model entry, not an implementation detail: the DB writes path
names and access patterns to disk. The accepted mitigations are rollups-only, no
content, `0600`/`0700` permissions, retention pruning (`prune`, `:287`), the
`--no-history` opt-out, and the rule that `~/.janusfs` is never itself a mount
source.

`Open` (`:40`) is failure-tolerant by design: `startMount` logs a warning and
continues without persistence if it errors (`cmd/janusfs/runtime.go:112`). A
mount must never fail because history is unavailable.

# HTTP surface

`api.New(uiFS, token, registry, hist)` (`internal/api/server.go:49`) builds one
`http.ServeMux`. Every `/api/v1/*` route is wrapped in `withToken`, checking the
per-mount bearer token generated at `startMount` (`runtime.go:88`).

| Route | Purpose |
|---|---|
| `/api/v1/summary` | live snapshot: counts, states, generation, uptime |
| `/api/v1/coverage` | current masked and hidden paths with the matching rule |
| `/api/v1/reveal` | resolve one path and return its derivation |
| `/api/v1/config` | read and write the config files through the dashboard editor |
| `/api/v1/reload` | trigger `mountRuntime.reload` |
| `/api/v1/history` | rollup series |
| `/api/v1/sessions` | past mount sessions |
| `/api/v1/vfsmeta/status.json`, `/api/v1/vfsmeta/conflicts.json` | the same content the synthetic `.janusfs` files serve |

The server is a thin adapter: it holds closures injected by `startMount`
(`SetMountInfo`, `SetVFSMeta`, `SetReload`, `runtime.go:124`) rather than
importing the engine or provider itself. `SetVFSMeta` takes a resolver function
`func(relPath string, isDir bool) (decision string, patterns []string, ruleRef string)`,
which is how the dashboard gets decisions without a second resolution path.

The dashboard is served from an `embed.FS` (`internal/ui/embed.go`), so the
binary has no external asset or CDN dependency.

# Multiplexing

There is one listener for the whole daemon on `127.0.0.1:<ui-port>`. Per-mount
API servers are reached through `/mounts/<uuid>/...`, with the daemon stripping
the prefix before delegating (`cmd/janusfs/daemon.go:560`). See
[CLI and daemon](cli-and-daemon.md).

# Where the API and the FUSE view disagree

`status.json` carries no watcher field: there is no file watcher (FR-20), so
reloads happen on demand and there is no liveness to report.
