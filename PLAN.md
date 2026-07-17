# JanusFS — Secure, Type-Aware FUSE File System

**Version:** 2.0 (design-locked)
**Status:** Ready for engineering
**Scope of this plan:** macOS only, via FUSE-T. Linux is a deliberate later port behind an OS-abstraction seam; nothing in this plan should be written in a way that blocks it, but Linux is out of scope for the MVP and the first mature version.
**Language:** Go.

---

## 1. What JanusFS Is

JanusFS is a user-space file system that sits between an AI agent (or any process) and the host file system, presenting a **sanitized view** of a directory tree. It enforces three access states per path, driven by `.janusignore` and `.janusmask` config files that use familiar `.gitignore`-style semantics.

| State | Appears in listing | `getattr` size | `read` | `write` |
|-------|--------------------|----------------|--------|---------|
| **Allowed** | yes | real | native passthrough | native passthrough |
| **Masked** | yes | **real** (size-preserving redaction) | `*`-redacted content | **EACCES** (read-only) |
| **Hidden** | yes | real | **EACCES** | **EACCES** |

**Precedence (strict):** `Hidden > Masked > Allowed`.
**Fail-closed:** any rule-resolution or parser error for a path resolves that path to **Hidden**.

### Design invariants (these drive every decision below)

1. **Masking is byte-length preserving.** Each redacted byte becomes `*` (0x2A). Reported size always equals served bytes, so FUSE/NFS offset semantics are never violated and no tool sees a short/long read. Multibyte runs are masked at the byte level; a match spanning a read-chunk boundary is handled by carry-over of up to `maxMatchLen` bytes.
2. **Redaction is off the hot path.** Redacted content is computed on *change events*, not per read. Steady-state masked reads are passthrough from a cache/shadow. This keeps Go's GC idle on the hot path and makes the language choice performance-neutral (the latency floor is the FUSE-T NFS transport, not userspace CPU).
3. **Hidden means inaccessible, not invisible.** Existence and real size are shown (less surprising to tooling); contents are denied. `open`/`read`/`write` → EACCES.
4. **Observability is a first-class MVP feature**, not an add-on. The engine emits a structured event + metrics stream from day one; the UI is a consumer.

---

## 2. Configuration Files

### 2.1 `.janusignore` — pure `.gitignore` syntax (Hidden set)
Glob patterns, directory exclusions, negation (`!`). Hierarchical: traversal walks upward from the target to the mount root, discovering `.janusignore` at each level. Deeper configs override shallower ones; negation can re-include. `.git` is **not** ignored by default.

### 2.2 `.janusmask` — pattern-based redaction (Masked set)
Two dimensions: *which files* (glob) and *what inside them* (named pattern or custom regex).

```
# .janusmask
# <file-glob> : <pattern>[, <pattern>...]
# pattern = <built-in-name> | /<regex>/
# A glob with NO pattern masks the WHOLE file.

*.env            : env-value
**/*.pem         : whole-file
config/*.yaml    : /(password|secret):\s*(.+)/
**/credentials   : aws-key
secrets/*                                   # no pattern -> whole-file mask
```

- Custom regex is compiled with Go's `regexp` (RE2, linear-time — satisfies the anti-ReDoS requirement).
- The **masked span** is capture group 1 if present, else the whole match.
- Every masked span is replaced with `*` × len(matchedBytes) → size preserved.

### 2.3 Built-in pattern library (shipped, versioned, referenceable by name)

| Name | Masks |
|------|-------|
| `env-value` | RHS of `KEY=value` in dotenv files |
| `aws-key` | `AKIA…`/`ASIA…` IDs + secret access keys |
| `private-key` | PEM private-key blocks |
| `jwt` | `eyJ…` three-segment tokens |
| `db-uri` | credentials + host in `scheme://user:pass@host` URIs |
| `github-token` | `ghp_`, `gho_`, `ghs_`, … |
| `generic-secret` | `password:` / `secret:` values, high-entropy strings |
| `whole-file` | every byte → `*` |

### 2.4 Precedence resolution
For a path, compute Hidden-match and Masked-match from the merged hierarchical rule set. If Hidden matches → Hidden (wins). Else if Masked matches → Masked. Else Allowed. Any error at any step → Hidden.

---

## 3. Architecture

```
        AI Agent / process
              │  file ops (open/read/write/readdir/getattr)
              ▼
    ┌─────────────────────────┐
    │  Mount Adapter (FUSE-T)  │   OS-abstraction seam: Mounter + Watcher interfaces
    └─────────────────────────┘
              │
              ▼
    ┌─────────────────────────┐
    │     Decision Engine      │──emits──▶ Event Bus (in-proc)
    │  (rule resolve + route)  │
    └─────────────────────────┘
        │ allowed        │ masked                 │ hidden → EACCES
        ▼                ▼
   passthrough fd   RedactedContentProvider
                        │  (interface)
                        ├── RAM cache (default)
                        └── tmpfs shadow (large files, later)
                        ▲
                        │ invalidate
                   Watcher (fsevents via fsnotify)

    Event Bus ──▶ Metrics registry ──┐
              ──▶ Ring buffer         ├─▶ local HTTP API + WebSocket ─▶ Web UI (embed.FS, localhost)
              ──▶ Structured logs ────┘
```

### 3.1 Read path
- **Allowed:** forward to a real fd on the backing store; near-native.
- **Masked:** serve from `RedactedContentProvider`. First read (or post-invalidation read) computes redaction via streaming pass, populates cache, serves it. **Fail-closed during rebuild:** while a masked file's redacted content is being (re)computed, serve the prior redacted bytes or block — never the raw original.
- **Hidden:** EACCES on open/read.

### 3.2 Write path
- **Allowed:** passthrough write to backing store.
- **Masked / Hidden:** EACCES. (Eliminates the write-back/smudge-merge problem entirely for v1. Reprojection is explicitly a future consideration, not in scope.)

### 3.3 `RedactedContentProvider` interface
Single interface with two implementations so the read path is identical regardless of backing store:
- **RAM cache** (default): thread-safe map keyed by path+mtime+size; redacted bytes held in memory. No secret-adjacent artifacts touch disk.
- **tmpfs shadow** (large-file path, first mature version): redacted copy materialized to a `0700` tmpfs/ramfs dir; served via passthrough fd. Torn down on unmount; never on persistent storage.
Selection by size threshold. Both share the same invalidation + fail-closed-rebuild logic.

### 3.4 Change detection
- `fsnotify` (fsevents on macOS) watches backing files and config files.
- Config change → recompile affected rules on a worker goroutine; never block FS ops during recompile (serve prior compiled rules until swap).
- Data change on a masked file → invalidate its cache/shadow entry.
- fsevents can coalesce/drop under load, so the read path **also** compares real mtime/size vs cached; on mismatch it re-redacts synchronously or denies rather than trusting the watcher alone.

### 3.5 Observability layer
- **Metrics** (global + per-path): counts for read/write/lookup/getattr/readdir; decision breakdown (allowed/masked/hidden); mask-pattern hit counts; cache hit/miss; redaction latency histogram; bytes served; watcher event rate.
- **Event feed:** each decision as `{ts, op, path, decision, matchedPattern, bytes, latencyµs}` pushed to a bounded ring buffer and streamed over WebSocket.
- **Logs:** structured JSON to stderr; runtime conflict warnings surfaced here too.
- **No Prometheus in MVP.** JSON API + UI only. OpenTelemetry is a later option, gated on measured performance (added only if it doesn't degrade the hot path).

### 3.6 UI (embedded, MVP) — high-level operational dashboard
Served by the Go binary on `localhost` only; assets embedded via `embed.FS` (single binary, no Node build step). Vanilla JS + HTMX + WebSocket.

For now the dashboard is a **high-level overview**, not a per-file inspector. It answers: what's being protected, how hard is it being hit, and what is JanusFS costing. Panels:
- **What is masked** — list of paths currently resolving to Masked + which pattern matched them, with a total count.
- **What is ignored (Hidden)** — list of paths currently resolving to Hidden + the rule that hid them, with a total count.
- **Serve frequency** — how often masked vs hidden vs allowed paths are being served (counts / rate), including a "top N most-served" ranking so hot files are obvious.
- **Latency due to the tool** — overhead JanusFS adds: passthrough overhead vs native, masked-read latency (cache hit vs rebuild), redaction latency — shown as current + rolling percentiles.

Deferred to later (not now): per-file masked-vs-real diff inspector, config editor, conflict-report UI (the conflict data still exists via `.janusfs/conflicts.json` and `janusfs doctor`).

### 3.7 Diagnostics surfaces
- Virtual file `.janusfs/conflicts.json` at mount root: real-time conflicting directives, parsed rules, memory footprint.
- `janusfs check`: static analysis of config trees — regex compiles, globs that match nothing, redundant/conflicting rule pairs.
- `janusfs doctor [--verbose]`: active-rule checks, loop/conflict detection, memory footprint, watcher/queue health.

---

## 4. CLI Surface

| Command | Purpose |
|---------|---------|
| `janusfs init` | Seed `.janusignore` + `.janusmask` with secure defaults (built-in patterns on). |
| `janusfs mount <src> <mountpoint> [--ui-port N]` | Mount sanitized view; start observability server + UI. |
| `janusfs umount <mountpoint>` | Unmount, tear down shadow dir, flush. |
| `janusfs check` | Static config linter. |
| `janusfs doctor [--verbose]` | Runtime health + conflict report. |

---

## 5. Performance Budget (verified via the observability layer)

- **Passthrough read/write:** within a small, measured overhead of native macOS FS on the same tree (baseline set empirically; FUSE-T NFS transport is the floor).
- **Steady-state masked read (cache hit):** within passthrough + fixed cache-lookup cost.
- **Redaction (cache miss / rebuild):** off hot path; bounded, reported in the latency histogram.
- These become CI/gate checks, not guesses. Language perf is not a gate — the transport dominates.

---

## 6. Phased Delivery

### Phase 0 — Walking skeleton
FUSE-T mount on macOS via `hanwen/go-fuse` adapter. Passthrough read + write for all files. No rules yet. Proves the mount plumbing and native I/O path end-to-end. Basic structured logging.

### Phase 1 — Rule engine + three states
`.janusignore` parsing (gitignore semantics, hierarchical, negation). Hidden (EACCES) + whole-file masking. Precedence + fail-closed. `janusfs init`.

### Phase 2 — Pattern-based masking
`.janusmask` format + built-in pattern library + custom RE2 regex. Size-preserving `*` redaction with chunk-boundary carry-over. `RedactedContentProvider` interface with **RAM cache**. Masked = read-only (EACCES on write).

### Phase 3 — Change detection
`fsnotify`/fsevents watcher. Async config recompile (worker pool, non-blocking swap). Cache invalidation. Fail-closed rebuild + mtime/size backstop.

### Phase 4 — Observability + UI (MVP-completing)
Event bus, metrics registry, ring buffer, WebSocket + JSON API, embedded web UI — **high-level dashboard only**: what's masked, what's ignored, serve frequency (incl. top-N most-served), and tool-induced latency. `.janusfs/conflicts.json` virtual file. Per-file inspector and config/conflict UI deferred.

### Phase 5 — Diagnostics maturity
`janusfs check` (static linter) + `janusfs doctor`. Conflict/redundancy detection. Memory-footprint reporting.

**MVP = Phases 0–4.**

### First mature version (post-MVP, still macOS-only)
- tmpfs **shadow provider** for large files (size-threshold selection).
- Streaming redaction for multi-GB files without full buffering.
- Phase 5 diagnostics hardened.
- Performance budget wired as CI gates.
- Optional OTel export (only if it clears the perf budget).

### Explicitly out of scope (future)
- Linux port (libfuse + inotify) behind the same seam.
- Masked-file write-back via smudge/clean reprojection (3-way merge).
- Prometheus endpoint.

---

## 7. Key Libraries

| Concern | Library |
|---------|---------|
| FUSE | `jacobsa/fuse` (documented FUSE-T support; Phase 0 spike validates; libfuse-t cgo as fallback) |
| gitignore matching | mature Go gitignore lib (chosen at Phase 1 against a `git check-ignore` conformance suite) |
| Regex (RE2, anti-ReDoS) | stdlib `regexp` |
| File watching | `fsnotify` (fsevents) |
| Metrics registry + `/metrics` | `prometheus/client_golang` (no external Prometheus server required; OTel deferred — see SPEC §10) |
| Dashboard history store | SQLite via `modernc.org/sqlite` (pure Go; rollups only — see SPEC §3.8) |
| UI transport | stdlib `net/http` + `embed.FS`, WebSocket (`nhooyr.io/websocket` preferred) |

Full engineering contract, including agent implementation instructions: **`SPEC.md`**.

---

## 8. Security Notes

- Redacted bytes stay in RAM by default; the tmpfs shadow (later) is `0700`, non-persistent, and torn down on unmount.
- Any rule/parse error fails closed to Hidden.
- Watcher unreliability never opens a leak: read path independently re-verifies mtime/size before serving cached masked content.
- UI binds `localhost` only; real (unredacted) content in the inspector is gated behind an explicit user reveal.
