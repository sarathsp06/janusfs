---
type: Architecture
title: JanusFS architecture
description: Process topology, the per-operation request path, and how the daemon assembles one mount.
tags: [architecture, daemon, fuse, wiring]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: daemon
    resource: /cmd/janusfs/daemon.go
    title: daemon, control socket, dashboard multiplexer
  - id: runtime
    resource: /cmd/janusfs/runtime.go
    title: mountRuntime, startMount, stop
  - id: mount-darwin
    resource: /internal/mount/mount_darwin.go
    title: Adapter.Mount (darwin)
  - id: agents
    resource: /AGENTS.md
    title: AGENTS.md package table
---

# One-paragraph model

There is exactly one long-lived process, `janusfs daemon`. It owns every live
FUSE mount, one combined dashboard HTTP listener, and a Unix control socket.
Every other `janusfs` subcommand is a short-lived client that sends one JSON
object over that socket and exits. A mount is *not* a separate process: it is a
`mountRuntime` struct inside the daemon holding a decision engine, a redaction
cache, a FUSE server goroutine, and an HTTP handler.

# Process topology

```
janusfs mount|umount|update|path      (short-lived clients)
        │  one JSON object per connection
        ▼
   ~/.janusfs/daemon.sock
        │
┌───────┴──────────────────────────────────────────┐
│ janusfs daemon (single process)                   │
│                                                   │
│  mounts map[abs-mountpoint]*mountRuntime          │
│    ├── engine.Engine      (atomic rule snapshot)  │
│    ├── provider.RamCache  (redacted bytes, LRU)   │
│    ├── obs.Recorder ──▶ history.Store (SQLite)    │
│    ├── api.Server         (per-mount dashboard)   │
│    └── mount.Adapter ──▶ fuse.Server goroutine    │
│                                                   │
│  http 127.0.0.1:7381                              │
│    /                    combined index            │
│    /mounts/<uuid>/...   proxied to that mount's   │
│                         api.Server                │
└───────────────────────────────────────────────────┘
```

The socket path is `~/.janusfs/daemon.sock`, built by `socketPath()` in
`cmd/janusfs/daemon.go:605`. A second daemon refuses to start: `runDaemon`
dials the socket first and, if anything answers, prints the running daemon's
mount list and exits zero (`cmd/janusfs/daemon.go:99`). A socket that does not
answer is treated as stale and removed (`daemon.go:119`).

# The request path for one read

Every decision-bearing FUSE operation follows the same shape. Anchors are in
`internal/mount/janus_node.go`.

1. The kernel calls a `JanusNode` method. The node's path relative to the mount
   root comes from `relPath()` (`janus_node.go:109`), which walks the go-fuse
   inode tree; the real on-disk path comes from `absPath()` (`:114`), a plain
   `filepath.Join` of the root's source path.
2. `resolve()` (`:122`) calls `engine.Engine.Resolve(relPath, isDir)`. It wraps
   the call in a `recover()` that folds any panic to
   `Decision: Hidden, Poisoned: true` — the fail-closed invariant this package
   owns.
3. `engine.Resolve` (`internal/engine/engine.go:76`) loads the current
   `*rules.RuleSet` from an `atomic.Pointer` and delegates to
   `RuleSet.Resolve`. No lock is taken, so a concurrent `Reload` never blocks a
   reader. See [policy engine](policy-engine.md).
4. The node branches on the returned `Decision`:
   - `Allowed` delegates to the embedded `fs.LoopbackNode` (real passthrough).
   - `Masked` returns a `maskedHandle` with `fuse.FOPEN_DIRECT_IO` set
     (`:233`), so the kernel calls `Read` on every access instead of caching
     pages.
   - `Hidden` returns `syscall.EACCES`.
5. A masked `Read` (`:527`) re-resolves the decision *on every call* rather
   than trusting the decision captured at open time, then asks
   `provider.RamCache.ReadAt` for the redacted bytes. See
   [masking pipeline](masking-pipeline.md).
6. `observe()` (`:90`) emits a `mount.OpEvent` for the operation. The daemon
   adapts that into an `obs.Event` in `makeObserver`
   (`cmd/janusfs/runtime.go:211`).

The important structural property: the adapter never decides anything and never
redacts anything. It translates a Decision into a kernel answer. All policy
lives in `internal/rules`, all redaction in `internal/redact`.

# How one mount is assembled

`startMount` (`cmd/janusfs/runtime.go:73`) is the whole dependency-injection
story — manual, explicit, no framework:

```
engine.New(cfg.Src)                    compile the rule tree, generation 1
provider.NewRamCache(budget, perFile, redactBufferMax)
crypto/rand → 16-byte hex bearer token for this mount's API
history.Open(src, ~/.janusfs/history/<hash>.db, retentionDays)   unless --no-history
obs.NewRecorder(hist)
api.New(ui.FS, token, registry, hist)  + SetMountInfo/SetVFSMeta/SetReload
mount.Adapter{Engine, Provider, ErrorLogger, DebugLogger, OnMounted, Observe}
go adapter.Mount(ctx, src, mountpoint)  ← blocks until unmount
<-ready                                 ← OnMounted fired, or the mount failed
```

`startMount` returns only once the mount is actually serving. That is what makes
`janusfs mount` able to honestly report success: `OnMounted` fires after
`fs.Mount` returns and the kernel handshake completed
(`internal/mount/mount.go`, the shared adapter), and it sends `nil` on the `ready`
channel. If `adapter.Mount` returns before that, the error goes on the same
channel and `startMount` tears down (`runtime.go:161`).

History failure is deliberately non-fatal: `startMount` logs a warning and
continues without persistence (`runtime.go:112`).

# Teardown

`mountRuntime.stop()` (`runtime.go:172`) is the ordered shutdown:

1. cancel the context, which makes `Adapter.Mount`'s goroutine call
   `server.Unmount()` (`mount.go`, the shared adapter);
2. wait `shutdownGrace` for the serve loop to exit;
3. if it drags, `unmountKernel(mountpoint, force=true)` — on darwin a
   `diskutil unmount` → `umount` → `diskutil unmount force` ladder, on Linux
   `fusermount3 -u` → `fusermount -u` → `umount` → the lazy `-uz`/`-l`
   variants (`cmd/janusfs/umount.go:133` and `:160`);
4. close the recorder and the history store.

`stop` is nil-safe throughout so it doubles as the cleanup path for a mount that
failed partway through `startMount`.

This ladder only runs when the daemon executes its own shutdown path. A
`SIGKILL`ed or panicking daemon leaves the mount attached with no server behind
it — see [known gaps](known-gaps.md).

# Package dependency rule

The dependency direction is enforced by convention, not by tooling:

```
cmd/janusfs ──▶ config, logging, api, engine, provider, mount, obs, history, ui
mount       ──▶ engine, provider, apperrors, vfsmeta
engine      ──▶ rules
rules       ──▶ patterns
provider    ──▶ redact, patterns, apperrors
redact      ──▶ patterns
```

`provider` deliberately does **not** import `engine`: callers resolve a
Decision themselves and pass the compiled `[]*patterns.Pattern` in
(`internal/provider/provider.go:65`). That keeps the cache testable without a
rule tree and stops the cache from becoming a second resolution path.

`internal/mount` is the only package permitted to call `apperrors.ToErrno`, so
the errno mapping lives in exactly one place
(`internal/apperrors/apperrors.go:50`).

# Stale entries in AGENTS.md

The package table in [AGENTS.md](/AGENTS.md) lists `internal/watch` and
`internal/platform`. Neither exists. There is no file watcher at all — rule
reload is explicit, via `janusfs update` or the dashboard, and
`mountRuntime.reload()` (`runtime.go:53`) is the only entry point. Treat this
bundle as the current map where the two disagree.
