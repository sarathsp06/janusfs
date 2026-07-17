# JanusFS — Detailed Technical Specification

**Version:** 1.1 (derived from PLAN.md v2.0, design-locked; readability pass for implementers)
**Platform:** macOS only (FUSE-T). Linux deferred behind the OS-abstraction seam.
**Language:** Go (≥ 1.26).
**Companion docs:** `PLAN.md` (product-level plan), `AGENTS.md` (condensed conventions), `README.md` (pitch). This spec is the engineering contract.

**How to read this spec:** Part I says *what* must be built (requirements, numbered FR-x/NFR-x — these numbers are referenced in code comments, commits, and tests). Part II says *how* (architecture; the Go interfaces in §7–§9 are normative). Part III says *in what order* (phases with hard exit criteria). Part IV is the rulebook for whoever writes the code — read it in full before your first commit. If a term is unfamiliar, check §2 (definitions and FUSE glossary) before searching elsewhere.

---

## Part I — Requirements

### 1. Goals and Non-Goals

**Goals**
- G1. Present a sanitized, read-write view of a source directory to an AI agent, enforcing per-path states **Allowed / Masked / Hidden**.
- G2. Zero secret bytes ever cross the mount boundary for Masked/Hidden paths, including during config reloads, cache rebuilds, watcher lag, and crashes (fail-closed).
- G3. Near-native performance for Allowed paths; masked reads served at cache/passthrough speed in steady state.
- G4. First-class observability: a live, high-level dashboard showing what is masked/hidden, serve frequency, and tool-induced latency.
- G5. Single self-contained binary: FUSE server, watcher, HTTP API, and embedded UI.

**Non-Goals (this version)**
- Linux support (seam preserved; no Linux code shipped).
- Write-back to masked files (smudge/clean reprojection).
- Prometheus/OTel export.
- Per-file masked-vs-real diff inspector UI, config-editing UI.
- Network mounts as source; source must be a local directory.

### 2. Definitions

| Term | Meaning |
|---|---|
| **Source tree** | The real directory being protected (`<src>`). |
| **Mount point** | Where the sanitized view appears (`<mountpoint>`). |
| **Decision** | The resolved state of a path: `ALLOWED`, `MASKED`, or `HIDDEN`. |
| **Rule set** | The merged, compiled result of all `.janusignore`/`.janusmask` files from mount root down to a path's directory. |
| **Redaction** | Byte-length-preserving replacement of matched spans with `*` (0x2A). |
| **Provider** | A `RedactedContentProvider` implementation serving redacted bytes. |
| **Generation** | Monotonic counter incremented on every rule-set recompile; used to invalidate decision and content caches atomically. |

**FUSE/filesystem glossary** (for implementers new to filesystems — these terms appear throughout):

| Term | Meaning |
|---|---|
| **FUSE** | "Filesystem in Userspace": the kernel forwards filesystem operations (open, read, …) to our ordinary user-space process, which decides how to answer them. |
| **FUSE-T** | A macOS FUSE implementation that needs no kernel extension; it exposes our filesystem to the OS as a local NFS server. |
| `lookup` | Resolve one name inside a directory to a file (happens before almost any other op on a path). |
| `getattr` / attrs | Return a file's metadata: size, permissions, mtime — what `stat` shows. |
| `readdir` | List a directory's entries. |
| `xattr` | Extended attributes: named metadata key-value pairs attached to a file (macOS uses them heavily, e.g. quarantine flags). |
| `mmap` | Memory-mapped file access; reads arrive through the same read path, just triggered by page faults. |
| **errno** | The error code a syscall returns. Ones used here: `EACCES` = permission denied; `ENOENT` = no such file; `EIO` = I/O error. |
| **inode** | The filesystem's internal identity of a file, independent of its name; two hard links share one inode. |
| **mtime** | Last-modification timestamp. |
| **passthrough** | Our process forwards the operation directly to the real file on disk without transforming anything. |

### 3. Functional Requirements

#### 3.1 Mount & lifecycle
- **FR-1** `janusfs mount <src> <mountpoint>` mounts a sanitized view of `<src>` at `<mountpoint>` via FUSE-T. Both paths must exist; `<mountpoint>` must be an empty directory; `<src>` and `<mountpoint>` must not overlap (neither may be a prefix of the other). Violations abort with a non-zero exit and a clear message.
- **FR-2** The process runs in the foreground by default; `--daemon` is out of scope for MVP. `SIGINT`/`SIGTERM` triggers clean unmount, watcher shutdown, cache zeroization, and HTTP server drain (≤ 5 s, then force).
- **FR-3** `janusfs umount <mountpoint>` unmounts an active mount (via `umount`/`diskutil unmount` fallback) and signals the owning process if discoverable (pidfile at `~/.janusfs/run/<hash-of-mountpoint>.pid`).
- **FR-4** If FUSE-T is not installed, `mount` fails with a message linking install instructions; `janusfs doctor` detects and reports FUSE-T presence/version.

#### 3.2 Decision semantics
- **FR-5** Every path resolves to exactly one Decision using strict precedence `HIDDEN > MASKED > ALLOWED`.
- **FR-6** **Fail-closed:** any error during rule discovery, parse, compile, or evaluation for a path resolves that path to `HIDDEN` and emits a `decision_error` event.
- **FR-7** Behavior matrix (authoritative):

| Op | ALLOWED | MASKED | HIDDEN |
|---|---|---|---|
| `readdir` (entry listed) | yes | yes | yes |
| `lookup` / `getattr` | real attrs | real attrs (real size) | real attrs (real size) |
| `open(O_RDONLY)` | passthrough fd | virtual handle | **EACCES** |
| `read` | passthrough | redacted bytes | **EACCES** (open already denied) |
| `open(O_WRONLY/O_RDWR)`, `create`, `truncate` | passthrough | **EACCES** | **EACCES** |
| `write` | passthrough | **EACCES** | **EACCES** |
| `unlink`, `rename` (as source or target), `chmod`, `chown`, `utimens` | passthrough | **EACCES** | **EACCES** |
| `mkdir`, `rmdir` (dir itself Allowed) | passthrough | n/a (dirs aren't masked; see FR-9) | **EACCES** |
| `symlink`, `link`, `mknod` | passthrough (target re-resolved on access) | **EACCES** | **EACCES** |
| `readlink` | passthrough | passthrough (link *content* is a path, not file data) | **EACCES** |
| `mmap`-backed reads | same as `read` | same as `read` (served through read path) | n/a |
| `statfs` | passthrough | passthrough | passthrough |
| xattr ops (`getxattr`, `listxattr`, …) | passthrough | **read-only** (get/list pass; set/remove EACCES) | **EACCES** |

- **FR-8** Directories can be `HIDDEN` (entire subtree inaccessible: entries listed at the directory's own parent per FR-7, but `opendir`/`readdir` of the hidden dir itself → EACCES, and every descendant is HIDDEN regardless of deeper rules — a hidden ancestor cannot be re-allowed below).
- **FR-9** Directories cannot be `MASKED`; a `.janusmask` glob matching a directory applies to the files within it (equivalent to `dir/**`). `janusfs check` flags directory-matching mask globs and reports the rewrite.
- **FR-10** Symlinks whose *target* resolves inside the mount are decided by the target's own path rules at access time. Symlinks pointing outside `<src>` are served as dangling (ENOENT on follow) — the sanitized view must never become an escape hatch to unprotected paths.
- **FR-11** Hard links: a Decision is per-path, not per-inode. Two links to the same inode may have different decisions; this is accepted and documented (protecting content-by-inode is a non-goal; users hide/mask both paths).

#### 3.3 Configuration
- **FR-12** `.janusignore` uses gitignore semantics exactly: glob patterns, `**`, trailing-`/` directory patterns, `!` negation, `#` comments, escaping. Evaluation order matches git: files are checked against patterns from all levels, deeper files later (later match wins), with the FR-8 constraint that negation cannot resurface anything under a hidden directory.
- **FR-13** `.janusmask` format (line-oriented, UTF-8):
  ```
  <file-glob> [: <pattern>[, <pattern>...]]
  pattern := <builtin-name> | /<RE2-regex>/
  ```
  - No pattern ⇒ `whole-file`.
  - Multiple lines for the same glob accumulate patterns (set union).
  - Comments `#`, blank lines ignored. A literal `:` in a glob is escaped `\:`.
  - Custom regex is compiled with Go `regexp` (RE2). Compile failure ⇒ that **file's whole rule set at that level fails closed** (every path that level's globs would have touched → HIDDEN) + `config_error` event; other levels unaffected.
- **FR-14** Masked span = capture group 1 if the regex defines ≥ 1 group, else the whole match. Replacement is `*` × byte-length of the span. Overlapping matches from multiple patterns are unioned (a byte masked by any pattern stays masked).
- **FR-15** Hierarchical discovery: for path `P`, config files are read from mount root down to `dir(P)`. Deeper `.janusmask`/`.janusignore` entries take precedence per FR-12. The config files themselves (`.janusignore`, `.janusmask`) are **ALLOWED, read-only, by default** in the mounted view — agents may benefit from seeing the policy — and the user can hide them explicitly with a rule like any other file. Regardless of their state, they are **never writable through the mount** (EACCES on any write), so an agent cannot weaken its own sandbox.
- **FR-16** Built-in pattern library (names reserved; user regex may not shadow a builtin name):

| Name | Definition (RE2, applied per the noted mode) |
|---|---|
| `env-value` | line mode: `(?m)^\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.+?)\s*$` → group 1 |
| `aws-key` | `\b((?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16})\b` and `(?i)\baws_secret_access_key\b\s*[=:]\s*([A-Za-z0-9/+=]{40})` |
| `private-key` | `(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----` (whole match) |
| `jwt` | `\b(eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b` |
| `db-uri` | `\b[a-z][a-z0-9+.-]*://([^\s:@/]+:[^\s@/]+)@` → group 1 (user:pass) |
| `github-token` | `\b((?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,255})\b` |
| `generic-secret` | `(?im)\b(?:password|passwd|secret|token|api[_-]?key)\b\s*[:=]\s*["']?([^\s"']{6,})` → group 1 |
| `whole-file` | sentinel: mask every byte |

  Exact regexes are unit-tested against a fixture corpus (true positives + known false-positive traps) and versioned; changes to a builtin bump a `patterns_version` reported by `doctor` and the UI.
- **FR-17** `janusfs init` writes template `.janusignore` (hides: `*.pem`, `*.key`, `id_rsa*`, `*.p12`, `.aws/credentials`, `*.keychain`, …) and `.janusmask` (`*.env*: env-value`, `**/*: aws-key, github-token, private-key, jwt`, `**/application*.{yml,yaml,properties}: generic-secret, db-uri`). Refuses to overwrite existing files without `--force`.

#### 3.4 Hot reload & consistency
- **FR-18** Changes to any `.janusignore`/`.janusmask` under `<src>` are detected via fsevents and trigger an async recompile on a worker; FS ops are never blocked by recompilation — the previous compiled generation serves until the new one is atomically swapped.
- **FR-19** A generation swap invalidates the decision cache and all masked-content cache entries whose decision or pattern set changed (conservatively: all entries, MVP).
- **FR-20** Content changes (`write`/`rename`/`chmod` on real files, observed via fsevents **or** detected by the mtime/size/inode backstop check at read time) invalidate that file's cached redaction. **Fail-closed rebuild:** during re-redaction, concurrent reads are served from the *previous* redacted bytes if the pattern set is unchanged, else block until rebuild completes (bounded 10 s, then EIO). Raw source bytes are never served for a masked path under any interleaving.
- **FR-21** The watcher is advisory, never authoritative: every masked-file read validates `(mtime, size, inode)` against the cache key before serving.

#### 3.5 Observability
- **FR-22** Every FUSE decision-bearing op emits an event: `{ts, op, path, decision, matchedRule, matchedPattern, bytes, latencyUs, cache: hit|miss|rebuild|na}`.
- **FR-23** Metrics registry (in-process, atomic counters + histograms) exposes: op counts by type × decision; bytes served by decision; cache hit/miss/rebuild counts; redaction duration histogram; per-op FUSE handler latency histogram (this is "latency due to the tool"); watcher event counts + drop/overflow count; active rule counts (ignore/mask globs, patterns); rule-set generation + last-reload timestamp; per-path serve counts (top-N via bounded LRU counter map, N=1000 tracked → top 50 reported).
- **FR-24** HTTP server binds `127.0.0.1:<uiPort>` (default 7381, `--ui-port`). Endpoints:
  - `GET /` embedded dashboard.
  - `GET /api/v1/summary` — snapshot: counts, states, generation, uptime, memory (`runtime.MemStats` subset).
  - `GET /api/v1/coverage` — current masked paths (+ pattern) and hidden paths (+ rule). Computed lazily by walking *already-observed* paths (paths seen by lookup/readdir since mount), plus an optional `?scan=full` that walks the source tree on demand (bounded, streamed).
  - `GET /api/v1/top?n=50&by=reads|bytes` — most-served paths.
  - `GET /api/v1/latency` — histograms: passthrough read, masked read (hit/rebuild), redaction, per-op handler time; p50/p90/p99 + rolling 1-min/5-min windows.
  - `GET /api/v1/events` (WebSocket) — live event feed with backfill of the ring buffer (capacity 8192 events) on connect; slow consumers are disconnected, never block the bus.
- **FR-25** Dashboard panels (MVP scope, per PLAN §3.6): What is masked / What is hidden (lists + counts + matched rule/pattern), Serve frequency (rates by decision + top-N), Tool latency (percentile tiles + sparkline) — each with a **time-range toggle (live / 1h / 24h / 7d / 30d)** backed by the history store (FR-41/46), plus a **Sessions panel** (past mounts: when, how long, totals). Auto-reconnecting WebSocket; degraded read-only view from REST polling if WS unavailable.
- **FR-26** Structured JSON logs to stderr (`slog`): startup config summary, generation swaps, config errors, watcher overflow, denial events at `debug` level (rate-limited), panics.
- **FR-27** Virtual directory `<mountpoint>/.janusfs/` (synthesized by the FUSE adapter; does not exist on disk): contains `conflicts.json` (live conflict/redundancy report, parsed rule dump, memory footprint) and `status.json` (generation, uptime, counts). Read-only. It **is listed normally** in the root `readdir` (not hidden), and user rules can never mask or hide it.

#### 3.6 CLI & diagnostics
- **FR-28** `janusfs check [path]` (default cwd): statically parses the config tree; reports per finding: severity (error/warn/info), file:line, message. Findings: regex compile errors, unknown builtin names, globs matching zero files, rule shadowing (a deeper rule fully overriding a shallower one), redundant pairs (identical effect), directory-mask rewrites (FR-9), hidden-dir-negation attempts (FR-8). Exit 1 on errors, 0 otherwise. `--json` for machine output.
- **FR-29** `janusfs doctor [--verbose]`: FUSE-T installed + version; active mounts (from pidfiles); per-mount: generation, rule counts, memory footprint, cache entries/bytes, watcher health (event rate, overflow count), UI port reachability. `--verbose` adds the full compiled rule dump.

#### 3.7 User-experience requirements (day-1, all phases)

**CLI UX**
- **FR-30** Every CLI failure prints a one-line cause + a one-line remedy (e.g. FUSE-T missing → the exact `brew install fuse-t` command). No raw Go errors or stack traces reach the user (they go to the debug log).
- **FR-31** `janusfs mount` on success prints a concise status block: source, mountpoint, rule counts (hidden/masked globs), and the dashboard URL — the dashboard is one click away from the very first mount.
- **FR-32** `janusfs init` explains what it wrote and why in ≤ 10 lines (which patterns are on, how to preview effect via `janusfs check`).
- **FR-33** `janusfs check` output is designed to be read: findings grouped by file, sorted by severity, with file:line and a suggested fix per finding; `--json` for tooling.
- **FR-34** All commands support `--quiet` and exit codes suitable for scripting.

**Dashboard UX**
- **FR-35** First-run experience: with no traffic yet, every panel shows a meaningful empty state ("No reads yet — point your agent at `<mountpoint>`"), never blank boxes or NaN.
- **FR-36** Human-readable presentation: byte sizes (KB/MB), rates (ops/s), relative times ("12 s ago"), latency in µs/ms as appropriate; long paths middle-truncated with full path on hover/tap.
- **FR-37** Live feel with control: serve-frequency and latency panels update ≤ 1 s behind reality; the live tail is pausable; coverage lists are searchable/filterable by substring.
- **FR-38** Degraded modes are first-class: WebSocket loss → automatic REST polling with a visible "delayed" indicator; server gone → clear "mount is down" banner, auto-reconnect.
- **FR-39** Glanceable hierarchy: the four panels answer, in order, "am I protected?" (masked/hidden counts), "is it being used?" (serve rates), "what is it costing?" (latency tiles) — a user should get all three answers within 5 seconds of opening the page.
- **FR-40** The dashboard itself stays lightweight: initial load ≤ 200 KB total, no external CDN requests (fully offline-capable), renders usefully at laptop-half-screen width.

#### 3.8 Persistence decision — history store only (SQLite, pure Go)

The dashboard must show **historical data across sessions** (FR-41), so the MVP includes exactly one persistence component: the **history store**. Everything else remains memory/derived:
- Compiled rules — derived, rebuilt from config files in milliseconds. **Config stays as files, never a DB**: hierarchical per-directory, git-committable and reviewable next to the code they protect, editable with any editor, hot-reloadable.
- "What is masked/hidden" — the *live output of the rule engine*; the dashboard queries the engine (`/api/v1/coverage`). Never persisted as truth (only recorded as point-in-time session snapshots for history display).
- Redacted content cache — RAM only (NFR-1/NFR-6 unchanged).

**History store requirements:**
- **FR-41** The dashboard presents past data alongside live data: trends over time (serve rates by decision, bytes, latency percentiles), previous sessions (mount time, duration, rule counts, totals), historical top-N paths, and coverage snapshots ("what was masked when").
- **FR-42** Storage: **SQLite via `modernc.org/sqlite`** (pure Go — preserves single-binary goal G5; DuckDB rejected for MVP: cgo + tens-of-MB binary cost unjustified at this volume). One DB per mount at `~/.janusfs/history/<mount-hash>.db`, mode `0600`, parent dir `0700`.
- **FR-43** Contents are **rollups, never raw events and never file content**: 1-minute aggregate buckets `{ts, op×decision counts, bytes, latency histogram}`, per-path counters flushed per bucket (top-1000 only), session records, and coverage snapshots (path + rule/pattern name) taken at mount, at generation swaps, and at unmount. Raw event persistence is a future opt-in, not MVP.
- **FR-44** Writer is a dedicated goroutine consuming the event-bus fan-out; batched transactional flush every 15 s; a slow/failed flush never blocks FUSE handlers or the live UI (drop + `history_write_errors` metric). DB corruption on open → rename aside, start fresh, warn — history is never allowed to prevent a mount.
- **FR-45** Retention: 30 days default (`--history-retention`), pruned at startup and daily; `--no-history` disables persistence entirely (dashboard shows live-only with a notice). `janusfs doctor` reports DB size, bucket count, oldest record.
- **FR-46** History API: `GET /api/v1/history/series?metric=…&window=…&res=1m|1h`, `GET /api/v1/history/sessions`, `GET /api/v1/history/top?window=…`, `GET /api/v1/history/coverage?at=…` — same auth as all `/api/*` (§11).
- **Threat-model entry:** the history DB deliberately writes the protected tree's **path names and access patterns** to disk. Accepted trade-off for FR-41, mitigated by: rollups-only, no content, `0600`/`0700` perms, retention pruning, `--no-history` opt-out, and exclusion of `~/.janusfs` itself from any mount source.

### 4. Non-Functional Requirements

- **NFR-1 Security:** No unredacted byte of a MASKED/HIDDEN file may be observable through the mount, the HTTP API, the WebSocket feed, logs, or `.janusfs` virtual files. Events/logs carry paths and pattern names, never matched content. Cache memory is zeroed on eviction and shutdown (best effort; Go GC caveat documented).
- **NFR-2 Fail-closed under all faults:** parser error, watcher overflow, cache corruption, provider panic (recovered) ⇒ affected paths read as HIDDEN (EACCES), never raw.
- **NFR-3 Performance budget** (measured by the built-in latency histograms; baselines captured in Phase 0 and pinned in `bench/BASELINE.md`):
  - Allowed-file sequential read throughput ≥ 85% of raw FUSE-T passthrough baseline.
  - Handler-added latency (our code, excluding transport) p99 ≤ 250 µs for allowed ops, ≤ 500 µs for masked cache hits.
  - Redaction throughput ≥ 100 MB/s single-threaded on a 1 MB dotenv-like corpus.
  - Decision resolution (cache hit) ≤ 5 µs; (miss, 10-level hierarchy) ≤ 200 µs.
- **NFR-4 Memory:** RAM-cache budget default 256 MB (`--cache-max-bytes`), LRU eviction; single cached file > 64 MB (`--cache-max-file`) is refused in MVP → masked reads for such files use on-the-fly streaming redaction per read (slower, still correct; tmpfs shadow arrives in the mature version).
- **NFR-5 Concurrency-safe:** all shared state accessed via immutable-snapshot swap (rule sets) or fine-grained locks (cache entries); no FUSE handler may block on the watcher, recompiler, event bus, or any HTTP consumer.
- **NFR-6 Reliability:** panic in any FUSE handler is recovered → EIO + `panic` event; the mount stays up. Crash of the process leaves no sensitive artifacts (RAM-only cache in MVP).
- **NFR-7 Compatibility:** macOS 13+ (Ventura), Apple Silicon + Intel, FUSE-T ≥ 1.0.30. Single static binary per arch (universal binary optional).
- **NFR-8 Testability:** the entire engine (rules, masking, cache, watcher policy, events) is testable without a mount; FUSE is an adapter over `fs.FS`-like internal interfaces.

---

## Part II — Architecture

### 5. Process & package layout

Single process, single Go module `github.com/sarathsp06/janusfs`.

```
cmd/janusfs/            main; subcommand dispatch (mount, umount, init, check, doctor)
internal/config/        Config struct + Load()/Validate(): flags + env, single source of truth for all tunables
internal/logging/       slog wrapper: New(component string) *slog.Logger, consistent `component` attribute
internal/apperrors/     canonical error taxonomy + sentinels; single ToErrno(err) mapping (§13)
internal/engine/        Decision engine: resolve(path) -> Decision (pure, no I/O beyond config read-through)
internal/rules/         .janusignore/.janusmask discovery, parse, compile; generation management
internal/patterns/      built-in pattern library + custom regex wrapper; span finder
internal/redact/        streaming size-preserving redactor (chunk carry-over)
internal/provider/      RedactedContentProvider: ramcache (MVP), shadow (mature, stub)
internal/watch/         Watcher interface + fsnotify(fsevents) impl; debounce, overflow handling
internal/mount/         Mounter interface + FUSE adapter (jacobsa/fuse primary; see §6)
internal/obs/           event bus, ring buffer, JanusMetrics registry, top-N tracker
internal/history/       SQLite rollup store: Store type over a DB interface, batched writer, retention pruning, query API (FR-41…46)
internal/health/        liveness/readiness probe used by the API's /healthz and by `janusfs doctor`
internal/api/           HTTP server, REST + WebSocket handlers (thin: no business logic, delegates to engine/provider/history)
internal/ui/            embed.FS static assets (HTML/CSS/vanilla JS + HTMX)
internal/vfsmeta/       .janusfs virtual files (conflicts.json, status.json)
internal/check/         static linter shared by `check` and conflicts.json
internal/platform/      OS seam: mount + watch constructors (darwin build tags now; linux later)
bench/                  reproducible fio/dd-style benchmarks + BASELINE.md
testdata/               fixture trees, pattern corpus (positives + FP traps)
```

Dependency rule: `mount` and `api` depend on `engine`/`provider`/`obs`/`history`; nothing depends on `mount` or `api`. `engine` is pure and synchronous. `config`, `logging`, and `apperrors` are leaf packages depended on by everything, depending on nothing internal.

*Package-boundary and convention choices in this Part II (leaf config/logging/errors packages, thin API-layer-over-service-layer, repository-style DB access, a named metrics struct, a health-probe package) are adapted from a prior Go service's architecture (`sparrow`), scaled down for a single-process local CLI tool rather than a multi-tenant network service.*

### 6. FUSE adapter & the FUSE-T decision

- **Primary:** `jacobsa/fuse` — documented FUSE-T support on macOS, clean Go API, no cgo.
- **Risk & fallback:** `hanwen/go-fuse` speaks the Linux kernel wire protocol and is not a good FUSE-T citizen. If the Phase 0 spike shows `jacobsa/fuse` gaps under FUSE-T (locking, xattr, mmap), fallback is cgo bindings against `libfuse-t`.
- **Phase 0 spike acceptance:** mount, `ls -la`, `cat`, `dd` 1 GB sequential read/write, `git status` inside the mount, Finder browse, unmount — all clean; baseline numbers recorded.
- FUSE-T quirks to handle explicitly: NFS-side attribute caching (set conservative attr timeouts, 1 s; masked/hidden decisions are still enforced server-side on every open/read so attr caching cannot leak), `.DS_Store`/`._*` AppleDouble noise (served as normal Allowed files; excluded from top-N by default), no reliable `flock` semantics (document: advisory locks unsupported in MVP).

### 7. Decision engine

```go
type Decision uint8 // Allowed, Masked, Hidden

type Resolution struct {
    Decision   Decision
    RuleRef    string      // "src/.janusignore:12" — for events/UI
    Patterns   []patterns.CompiledPattern // non-nil iff Masked
    Generation uint64
}

type Engine interface {
    Resolve(relPath string, isDir bool) Resolution // never errors: errors are folded into Hidden + event
    Generation() uint64
}
```

**Resolution algorithm** for `P` (relative to mount root):
1. Fast path: decision cache lookup `(P, generation)` → hit returns in O(1).
2. Walk ancestors root→`dir(P)`; if any ancestor directory is HIDDEN → HIDDEN (FR-8 short-circuit).
3. Evaluate ignore rules (git-ordered, later wins) → hidden?
4. Evaluate mask globs; collect the union of matching pattern sets.
5. Apply precedence; on any internal error → HIDDEN + `decision_error` event.
6. Insert into decision cache (sharded map keyed by path, tagged with generation; whole cache dropped on generation bump).

**Compiled rule set** is an immutable snapshot (`atomic.Pointer[RuleSet]`). The recompiler builds a full new snapshot off-thread and swaps it; readers never lock.

### 8. Masking pipeline

#### 8.1 Span finding
Each `CompiledPattern` exposes `FindSpans(buf []byte, base int64) []Span` where `Span{Off,Len int64}` are absolute file offsets. Multi-pattern results are merged (sorted, coalesced union) — FR-14.

#### 8.2 Size-preserving streaming redactor
For files that don't fit the cache (NFR-4) and for initial cache population:

- Read the source in 256 KiB chunks. Maintain a **carry-over tail** of `maxMatchLen - 1` bytes (per pattern set; `whole-file` and `private-key` set `maxMatchLen = ∞` ⇒ those patterns force whole-file buffering mode — acceptable: PEM files are small; `whole-file` needs no matching at all, it's a pure `*`-fill of size bytes with zero buffering).
- For unbounded custom regexes (no computable max length), the redactor uses **line mode** if the pattern is line-anchored (`(?m)`), else falls back to whole-file buffering with a hard cap (`--redact-buffer-max`, default 512 MB; beyond that the file fails closed to HIDDEN + warning — documented limitation until the mature-version streaming work).
- Output invariant checked in code: `len(out) == len(in)` per chunk; violation ⇒ panic-recover ⇒ EIO + fail-closed.

#### 8.3 Provider

```go
type ContentKey struct{ Path string; MTimeNS int64; Size int64; Inode uint64; Gen uint64 }

type RedactedContentProvider interface {
    // ReadAt serves redacted bytes; must satisfy FR-20/21 (validate key, fail-closed rebuild).
    ReadAt(ctx context.Context, key ContentKey, p []byte, off int64) (int, error)
    Invalidate(path string)
    InvalidateAll()
    Stats() ProviderStats
}
```

**`ramcache` implementation (MVP):**
- Sharded LRU (`--cache-max-bytes`, per-entry ≤ `--cache-max-file`).
- Entry states: `ready(gen, bytes)`, `building(prev *bytes)`, `absent`.
- Read flow: validate key against live `stat` → hit+valid: copy out. Stale: transition to `building`, keep `prev` if pattern set unchanged (serve stale-redacted, FR-20), kick rebuild goroutine (singleflight per path), swap on completion. Oversized: bypass cache, stream-redact per read.
- Eviction zeroes the byte slice before release (NFR-1 best-effort).

### 9. Watcher

```go
type Watcher interface {
    Start(root string, onConfig func(path string), onData func(path string, op Op)) error
    Stats() WatchStats
    Close() error
}
```

- fsnotify/fsevents recursive watch on `<src>`.
- Config events (`.janusignore`/`.janusmask` create/write/remove/rename) → debounce 200 ms → recompile job on a single recompiler goroutine (queue depth 1, coalescing).
- Data events → `provider.Invalidate(path)` (cheap; no redaction work here).
- Overflow/error → `InvalidateAll()` + metric increment + warning event (safe: backstop check re-validates everything anyway).

### 10. Observability internals

- **Event bus:** single MPSC channel (cap 4096) drained by one fan-out goroutine into: metrics registry, ring buffer, optional debug log. FUSE handlers use non-blocking send; on full channel the event is dropped and `events_dropped` incremented — **handlers never block on observability** (NFR-5).
- **Metrics:** standard counters/gauges/histograms are implemented with **`prometheus/client_golang`** as the in-process registry (pure Go, atomic hot path, log-scale latency buckets 1 µs–1 s) rather than a hand-rolled registry. This yields a standard `GET /metrics` exposition endpoint for free (token-auth, localhost — for users who already run their own Prometheus/Grafana; **janusfs never requires or assumes an external Prometheus server**, and the embedded dashboard does not depend on one). Rolling 1-min/5-min windows for the live UI via 60×1 s slot rings on top of the registry.
  Metrics are grouped into one explicit, named struct (constructed once in `internal/obs`, passed by reference — not package-level globals), analogous to a per-application metrics catalogue:

  ```go
  type JanusMetrics struct {
      OpsTotal          *prometheus.CounterVec   // labels: op, decision
      BytesServedTotal  *prometheus.CounterVec   // labels: decision
      CacheResult       *prometheus.CounterVec   // labels: hit|miss|rebuild
      RedactionDuration prometheus.Histogram
      HandlerDuration   *prometheus.HistogramVec // labels: op
      WatcherEvents     *prometheus.CounterVec   // labels: kind, dropped
      ActiveRules       *prometheus.GaugeVec     // labels: ignore|mask
      Generation        prometheus.Gauge
  }
  ```
  This makes the metric surface reviewable in one place and trivially mockable in tests (pass a `JanusMetrics` built against a private registry).
- **Division of persistence:** standard metrics = Prometheus registry (live) + optional external scrape; implementation-specific dashboard data (per-path top-N, coverage snapshots, sessions, multi-day history) = SQLite rollups (§3.8) — per-path data as Prometheus labels would be a cardinality anti-pattern, and multi-day history must work with zero external infrastructure.
- **OpenTelemetry — considered and deferred (decision record):** janusfs is a single local process — no distributed tracing need, and the dashboard is self-contained. The OTel SDK would add a large dependency tree (threat-model cost) and heavier per-measurement machinery on a hot path with a ≤ 100 ns emit budget, for no MVP feature. Interop is preserved without it: an OTel Collector's Prometheus receiver can scrape `/metrics` into any OTLP backend with zero janusfs code changes. Revisit only on concrete user demand for native OTLP push, gated on the NFR-3 perf budget.
- **Top-N:** bounded count-min-sketch-backed LRU (track 1000 paths, report top 50 by reads and by bytes).
- **Ring buffer:** fixed 8192-slot circular buffer of serialized events; WebSocket subscribers get a snapshot then live tail; per-subscriber send buffer 256, overflow ⇒ disconnect.

### 11. HTTP API & UI

- `net/http` stdlib; WebSocket via `nhooyr.io/websocket` (small, maintained) or `gorilla/websocket` — pick at Phase 4 start, criteria: maintenance status.
- Bind `127.0.0.1` only; `Cache-Control: no-store` on all API responses.
- **Browser-origin hardening (day 1, non-negotiable):** localhost binding alone does not stop a malicious web page in the user's browser from scripting requests to `127.0.0.1:7381` (DNS rebinding / CSRF). Therefore: strict `Host` header validation (`127.0.0.1[:port]`/`localhost[:port]` only), `Origin` check on the WebSocket upgrade (same-origin or absent), and a per-mount random bearer token minted at startup — embedded in the dashboard URL printed by `janusfs mount` (FR-31) and required by all `/api/*` endpoints. The UI stores it after first load. Zero extra steps for the user; closes the rebinding hole.
- UI: static `index.html` + one JS module + HTMX for panel refresh; charts as inline SVG sparklines (no chart lib). Panels per FR-25. Dark, dense, ops-tool aesthetic.

### 12. Virtual `.janusfs` files

Implemented in the FUSE adapter as synthetic inodes rooted at `/.janusfs`. Content generated on `open` (snapshot), sized at open time (consistent `getattr`), read-only, `0444`. Never subject to user rules; never writable.

### 13. Error-handling matrix (canonical errno mapping)

| Condition | errno | Event |
|---|---|---|
| Read/write/open on HIDDEN | `EACCES` | `denied` |
| Write/open-for-write on MASKED | `EACCES` | `denied` |
| Rebuild timeout (10 s) | `EIO` | `rebuild_timeout` |
| Redactor invariant violation / handler panic | `EIO` | `panic` |
| Config parse failure (per FR-13) | paths → HIDDEN | `config_error` |
| Symlink escaping `<src>` | `ENOENT` on follow | `symlink_escape` |
| Oversized + unbounded-regex fail-closed | `EACCES` | `redact_unsupported` |

This table is implemented as code, not left as documentation-only: `internal/apperrors` defines one sentinel per row (`ErrHidden`, `ErrMaskedReadOnly`, `ErrRebuildTimeout`, …) and a single `ToErrno(err error) syscall.Errno` function. `internal/mount` is the **only** package permitted to call `ToErrno`; every other package returns a sentinel and lets the mount adapter translate it, keeping the errno mapping in exactly one place.

### 14. Security model (summary)

- **Trust boundary:** the mount point + the localhost HTTP surface. The agent is untrusted; the user operating the CLI is trusted.
- **Agent cannot weaken policy:** config files are read-only through the mount (FR-15); `.janusfs` virtuals are read-only; the HTTP API has no mutating endpoints in MVP.
- **Leak channels audited in review:** file sizes (accepted — real sizes shown by design), timing (cache hit vs miss distinguishable — accepted, documented), error text (must not embed file content), events/logs (path + pattern name only, never matched bytes), UI (shows redacted view only in MVP; no reveal endpoint exists), browser-origin attacks on the localhost API (mitigated per §11).
- **Security as a per-phase discipline, not a gate:** a written threat model is a Phase 0 deliverable and is updated at every phase exit; every phase's exit criteria include a security checklist item (see Part III). The **leak oracle** (sentinel secrets planted in fixtures; every byte read through the mount is scanned for them) is built in Phase 1 and runs in CI from then on — masking (Phase 2) and hot-reload races (Phase 3) land against an already-armed tripwire, not before it exists.
- **Persistence is exactly one artifact:** the history DB (rollups, paths + counts only, `0600`, opt-out via `--no-history`) plus the pidfile; see §3.8. No redacted or raw file content is ever written to disk.

### 15. Configuration, logging, and process wiring

**`internal/config`** is the single source of truth for every tunable in this spec (`--ui-port`, `--cache-max-bytes`, `--cache-max-file`, `--history-retention`, `--no-history`, `--redact-buffer-max`, …). One `Config` struct, populated from CLI flags (primary, since this is an interactive local tool — unlike a server, env vars are a secondary override, not primary), with a `Validate() error` that catches conflicts before any FUSE call is made (e.g. `<src>`/`<mountpoint>` overlap from FR-1). No package other than `cmd/janusfs` reads a flag or env var directly.

**`internal/logging`** wraps `slog`: `logging.New(component string) *slog.Logger` attaches a `component` attribute (`engine`, `provider`, `watch`, `api`, `history`, …) so every log line is attributable at a glance; one JSON handler configured once in `cmd/janusfs`. No package calls `slog.Default()` directly (enforced by the lint rule in §21).

**Process wiring (`cmd/janusfs mount`)** is manual, explicit dependency injection in `main.go` — no DI framework. Fixed order, mirroring the numbered-responsibilities style used for documenting server startup in prior work:

1. Parse flags → `config.Load()` → `Validate()`.
2. Construct `logging.New(...)` per component.
3. Construct `internal/obs` (event bus + `JanusMetrics` + ring buffer).
4. Construct `internal/history` store (or a no-op stub if `--no-history`).
5. Construct `internal/rules` (initial compile) → `internal/engine`.
6. Construct `internal/provider` (ramcache) wired to the engine's generation.
7. Construct `internal/watch`, wired to trigger rule recompilation and provider invalidation.
8. Construct the `internal/mount` FUSE adapter over engine + provider + apperrors.
9. Construct `internal/api` (REST + WebSocket + `/healthz` + `/metrics`), wired read-only to obs/history/engine.
10. Start: mount (blocking call in a goroutine), then API server; print the FR-31 status block with the dashboard URL.
11. Install signal handler → on `SIGINT`/`SIGTERM`: unmount, stop watcher, flush + close history, drain API server (≤ 5 s), zero caches, exit.

**`internal/health`** exposes a single `Prober` used two ways: internally by `GET /healthz` (so the UI can show the FR-38 "mount is down" banner reliably) and by `janusfs doctor` connecting to a running mount's API. A probe checks: mount responds to a synthetic stat within a timeout, watcher goroutine alive, history writer alive (if enabled). This is a liveness check for a single local process, not a distributed-systems readiness gate — deliberately small.

### 16. History store implementation pattern

`internal/history` is built the same way as a typical service's DB repository layer, scaled to one embedded SQLite file:

```go
type DB interface {
    ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
    QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    Close() error
}

type Store struct {
    db DB
}

func (s *Store) WithTx(ctx context.Context, fn func(DB) error) error // wraps BEGIN/COMMIT/ROLLBACK
```

- All SQL lives behind `Store` methods (`RecordBucket`, `RecordSession`, `RecordCoverageSnapshot`, `QuerySeries`, `QuerySessions`, `QueryTop`, `Prune`) — no raw SQL anywhere else in the codebase (mirrors the "no direct SQL outside the repository" convention).
- SQLite errors are translated once, at the `Store` boundary, into `internal/apperrors` sentinels (constraint violation → `ErrAlreadyExists`, no rows → `ErrNotFound`) rather than leaking driver-specific errors upward.
- The batched writer (FR-44) calls `Store` methods inside a single `WithTx` per 15 s flush; it never touches `*sql.DB` directly.
- Tests use an in-memory SQLite (`:memory:`) `Store` — the interface makes `internal/api`'s history-endpoint tests independent of a real file.

---

## Part III — Delivery Plan

### 17. Phased delivery

Security and UX are standing workstreams: **every phase exit includes (a) a security checklist item and (b) a UX checklist item.** The dashboard exists in minimal form from Phase 1 onward so progress is always visible, not only tested. **MVP = Phases 0–4.** A phase's exit criteria are hard gates (see §20.3).

#### Phase 0 — Walking skeleton & platform validation (exit: baseline numbers in `bench/BASELINE.md`)
1. Repo scaffold, module, CI (macOS runner: build + unit tests; FUSE-T integration job on self-hosted/local script).
2. `internal/platform` seam; `jacobsa/fuse` passthrough adapter (all ops → source tree; no rules).
3. Spike acceptance suite (§6): ls/cat/dd/git/Finder/unmount + 1 GB throughput; record baselines.
4. Signal handling, clean unmount, pidfile; `janusfs mount/umount` CLI skeleton; `slog` JSON logging.
5. **Security:** written threat model (`docs/THREAT_MODEL.md`) — boundaries, assets, leak channels, per-phase checklist template.
6. **UX:** FR-30 error style guide implemented in the CLI skeleton (incl. FUSE-T-missing remedy); FR-31 status block stub.
   **Go/No-Go:** if `jacobsa/fuse` fails under FUSE-T → switch to libfuse-t cgo fallback before proceeding.

#### Phase 1 — Rule engine & three states
1. `internal/rules`: gitignore parser/matcher (evaluate `go-git/gitignore` vs `sabhiram/go-gitignore` against a git-conformance fixture suite; wrap, don't fork).
2. Hierarchical discovery + immutable snapshot + generation counter (no watcher yet; recompile on manual SIGHUP for testing).
3. `engine.Resolve` + decision cache + fail-closed folding; FR-7 matrix in the adapter (HIDDEN → EACCES; whole-file masking only: `*`-fill of real size — zero buffering).
4. FR-8/9/10/11 semantics + tests (hidden dirs, symlink escape, hardlink documentation test).
5. `janusfs init` templates (+ FR-32 explanatory output).
6. **Security:** leak oracle built and wired into CI (sentinel secrets in fixtures; every mounted read scanned) — armed before pattern masking exists.
7. **UX:** minimal dashboard v0 ships now — single page (localhost, token-auth per §11) showing decision counts, hidden/masked path lists, and a raw event tail from a basic event bus. Ugly is fine; empty states (FR-35) are not optional.
   **Exit:** conformance test suite: a fixture tree + expected-decision table, run through both the pure engine and a real mount; leak oracle green; dashboard v0 reachable from the FR-31 status block URL.

#### Phase 2 — Pattern-based masking
1. `internal/patterns`: builtin library (FR-16) + corpus tests (positives, FP traps); custom `/regex/` parsing + reserved-name enforcement.
2. `.janusmask` parser (FR-13) incl. escaping, accumulation, error fail-closed scoping.
3. `internal/redact`: chunked size-preserving redactor with carry-over; property tests (`len(out)==len(in)` for random inputs; idempotence; chunk-boundary match torture tests incl. multibyte UTF-8 straddles).
4. `provider/ramcache`: LRU, singleflight rebuild, stale-serve, oversize bypass, zero-on-evict.
5. Wire masked reads through the adapter; masked-write EACCES; xattr read-only rule.
6. **Security:** adapter + provider security review; leak oracle extended with pattern-specific sentinels (planted AWS keys, JWTs, PEM blocks must never survive to the mount).
7. **UX:** dashboard v0 gains matched-pattern names on the masked list; `janusfs check` findings readable per FR-33.
   **Exit:** end-to-end: agent `cat`s a `.env` through the mount and sees `*`s of identical byte length; `dd` offset reads line up; security review passes; leak oracle green.

#### Phase 3 — Change detection
1. `internal/watch` fsevents impl; debounce; overflow → `InvalidateAll`.
2. Recompiler goroutine + generation swap + cache drop (FR-18/19).
3. mtime/size/inode backstop on every masked read (FR-21); fail-closed rebuild path (FR-20) + rebuild-timeout EIO.
4. Race tests: `-race` stress harness — writer mutates source + configs while N reader goroutines hammer the mount; the Phase 1 leak oracle is the assertion layer (no raw byte ever appears in any read).
5. **Security:** threat model updated for the reload/rebuild race surface; kill -9 artifact audit.
6. **UX:** dashboard v0 shows rule-set generation + "config reloaded Ns ago", making hot-reload visible and trustworthy to the user.
   **Exit:** stress suite green under `-race` for 10 min; kill -9 during rebuild leaves no artifacts.

#### Phase 4 — Observability + dashboard (completes MVP)
1. `internal/obs`: event bus, metrics, histograms, rolling windows, top-N, ring buffer; non-blocking emit from handlers (benchmark: emit ≤ 100 ns happy path).
2. `internal/history`: SQLite rollup store — schema + migrations, batched 15 s writer off the event bus, session records, coverage snapshots, retention pruning, corruption-safe open, `--no-history` (FR-41…46).
3. `internal/api`: REST endpoints (FR-24) + history endpoints (FR-46) + WebSocket feed; localhost bind; no-store headers.
4. `internal/ui`: embedded dashboard — the four panels with live/1h/24h/7d/30d time-range toggles + Sessions panel (FR-25) with auto-reconnect + REST fallback.
5. `.janusfs/conflicts.json` + `status.json` virtual files (`internal/vfsmeta`, fed by `internal/check`).
6. Latency budget verification (NFR-3) against Phase 0 baselines; publish results in `bench/`.
7. **Security:** browser-origin hardening lands in full (§11: Host validation, WS Origin check, per-mount bearer token); API/WS reviewed against the leak-channel list; history DB reviewed (perms, rollups-only content, prune, `--no-history`); threat model final pass for MVP.
8. **UX:** dashboard v0 replaced by the full four-panel dashboard meeting every FR-35…FR-40 criterion; a first-run walkthrough test (fresh machine → install → init → mount → dashboard) performed and friction items fixed.
   **Exit (MVP):** demo script — mount a fixture repo, run an agent-like reader, watch the dashboard show masked/hidden coverage, serve rates, and tool latency live; all NFR-3 gates pass; FR-35…FR-40 verified panel by panel.

#### Phase 5 — Diagnostics maturity (first mature version, part 1)
`janusfs check` full linter (FR-28), `janusfs doctor` (FR-29), shadowing/redundancy analysis shared with conflicts.json.

#### Phase 6 — Large-file & streaming maturity (first mature version, part 2)
tmpfs-equivalent shadow provider on macOS (RAM-disk via `hdiutil attach -nomount ram://` or accepted `0700` tmp dir tradeoff — decide with a spike), size-threshold provider selection, streaming redaction for unbounded patterns replacing the 512 MB fail-closed cap, perf gates wired into CI.

### 18. Test strategy (cross-phase)
- **Unit:** rules conformance vs git behavior; pattern corpus; redactor property tests; cache state machine.
- **Integration (mountless):** engine + provider + watcher against a temp dir, no FUSE.
- **Integration (mounted):** real FUSE-T mount in CI-adjacent script; FR-7 matrix executed via actual syscalls; leak-oracle stress.
- **Bench:** `bench/` reproducible scripts; baselines pinned; NFR-3 asserted.

### 19. Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| `jacobsa/fuse` gaps under FUSE-T | Medium | Phase 0 spike + libfuse-t cgo fallback decided before Phase 1 |
| FUSE-T NFS attr caching serves stale metadata | Medium | Conservative attr TTLs; enforcement is per-op server-side, so no security impact — only freshness |
| fsevents coalescing under heavy churn | High (by design) | FR-21 backstop makes watcher advisory-only |
| Regex false positives over-masking code | Medium | Corpus with FP traps; `check` warnings; users can narrow globs |
| Unbounded custom regex on huge files | Low | 512 MB cap → fail-closed + explicit event; Phase 6 removes limit |
| gitignore library semantic drift from git | Medium | Conformance fixture suite run against `git check-ignore` as oracle |

---

## Part IV — Implementation Instructions for the Coding Agent

These are operating instructions for any agent (or human) writing JanusFS code. They are binding. Where these instructions and code convenience conflict, these instructions win.

`AGENTS.md` at the repo root is the condensed, fast-loading version of this Part (quick commands, package table, code conventions) — read it first for orientation, then come back here for the full rationale on anything non-obvious.

### 20. Ground rules

1. **This spec is the contract.** Every behavior you implement must trace to an FR/NFR number. If you need a behavior the spec doesn't define, do not invent it silently — add a proposed amendment to `docs/SPEC_AMENDMENTS.md` with rationale, implement the most conservative (fail-closed) interpretation, and flag it in your report.
2. **Fail-closed is the tiebreak for every ambiguity.** When two readings are possible, pick the one where the agent behind the mount sees *less*.
3. **Phase order is strict.** Do not start phase N+1 work before phase N exit criteria are demonstrably met (tests + the phase's security and UX checklist items). The Phase 0 go/no-go on `jacobsa/fuse` under FUSE-T is a hard stop: if the spike fails, stop and report — do not begin the cgo fallback without a human decision.
4. **Dependency policy:** allowed without asking: `jacobsa/fuse`, `fsnotify`, `modernc.org/sqlite`, `prometheus/client_golang`, `caarlos0/env/v11` (env-var struct-tag parsing, `internal/config` only — see `docs/SPEC_AMENDMENTS.md` 2026-07-16), `spf13/cobra` + `spf13/pflag` (CLI subcommand dispatch/flags/help, `cmd/janusfs` only — see `docs/SPEC_AMENDMENTS.md` 2026-07-17), one WebSocket lib (`nhooyr.io/websocket` preferred), one gitignore lib (chosen per Phase 1 task 1), stdlib. Anything else requires a decision record in `docs/SPEC_AMENDMENTS.md` first. Never add: cgo deps (except the approved FUSE fallback), network-calling libs, telemetry SDKs.
5. **Never weaken these invariants, in any commit, even temporarily:** localhost-only binding; token auth on `/api/*` and `/metrics`; EACCES matrix (FR-7); size-preserving redaction (`len(out)==len(in)`); no file content in logs/events/DB; config files read-only through the mount; `~/.janusfs` perms (`0700`/`0600`).

### 21. Repository conventions

- Module path: `github.com/sarathsp06/janusfs`; Go ≥ 1.26; `gofmt` + `go vet` clean at every commit.
- Package layout exactly as §5; enforce the dependency rule (`engine` pure; nothing imports `mount` or `api`) — add a `depguard`/`go list` check in CI.
- Errors: wrap with `%w`, sentinel errors defined in `internal/apperrors` (§13); errno mapping only in `internal/mount` via `apperrors.ToErrno` (single translation point) — no other package constructs a `syscall.Errno`.
- No `panic` for control flow. FUSE handlers wrap in `recover` → EIO + `panic` event (NFR-6).
- Logging only via `internal/logging.New(component)` (§15); no bare `slog.Default()`, no `fmt.Print*` outside `cmd/`. Log messages must never include file content (NFR-1) — there is a lint test for this: any `slog` call in `redact`/`provider` taking a byte slice fails review.
- Config only via `internal/config` (§15); no package reads a flag or `os.Getenv` directly except `cmd/janusfs`.
- **Thin adapters, no logic in transport layers:** `internal/mount` and `internal/api` handlers translate requests and translate errors — they must not contain decision logic, redaction logic, or SQL. That all lives in `engine`/`provider`/`redact`/`history`, callable and testable without a mount or an HTTP server.
- **No direct SQL outside `internal/history`'s `Store` methods** (§16); a raw `"SELECT"` string anywhere else fails review.
- Every exported symbol in `internal/` has a doc comment referencing its FR where applicable (e.g. `// Resolve implements FR-5..FR-11.`).

### 22. Testing requirements (Definition of Done per task)

A task is done only when all of:
1. Unit tests for the new logic, table-driven where natural; property tests where the spec states an invariant (redactor: `len(out)==len(in)`, idempotence, chunk-boundary torture incl. multibyte straddles).
2. The **conformance suite** (fixture tree + expected-decision table, Phase 1 onward) passes against both the pure engine and a real mount.
3. The **leak oracle** (Phase 1 onward) passes: sentinel secrets planted in `testdata/` never appear in any byte read through the mount. New features add new sentinels (Phase 2: planted AWS keys, JWTs, PEM blocks).
4. `go test -race ./...` clean; mounted integration tests behind a build tag (`//go:build fuseintegration`) with a `make integration` target (requires FUSE-T locally).
5. `janusfs check`/`doctor`/dashboard reflect the feature where the spec says they must (a feature invisible to observability is incomplete — FR-22/25).
6. Benchmarks updated when touching hot paths (`engine.Resolve`, provider `ReadAt`, event emit) and compared against `bench/BASELINE.md`; regressions beyond NFR-3 budgets block the task.

### 23. Working style

- Small vertical slices: prefer "one FR end-to-end with tests" over broad scaffolding commits. Commit messages reference FR/phase (`Phase 2: FR-14 span union + tests`).
- Before writing a component, re-read its spec section AND its interface consumers; the interfaces in §7–§9 are normative — change them only via amendment.
- When a third-party library's behavior is load-bearing (gitignore semantics, fsevents delivery), write a characterization test capturing what we rely on, so upgrades that change behavior fail loudly.
- Keep the dashboard runnable at all times from Phase 1 (`janusfs mount … --ui-port`); manual smoke: mount `testdata/demo-tree`, open dashboard, verify the phase's UX checklist item.
- **Stop and ask a human when:** Phase 0 go/no-go fails; a spec invariant appears impossible on FUSE-T; a security review finding has no in-spec fix; two FRs genuinely conflict. Otherwise, do not block on questions — take the fail-closed reading and record the amendment.

### 24. Suggested execution order (first sessions)

1. Scaffold: module, `cmd/janusfs` with subcommand skeleton + FR-30 error style, Makefile (`build/test/integration/bench`), CI workflow, `testdata/demo-tree` fixture, `docs/THREAT_MODEL.md` skeleton, `docs/SPEC_AMENDMENTS.md`.
2. Phase 0 spike: passthrough FS on `jacobsa/fuse`; run the §6 acceptance list manually + scripted; write `bench/BASELINE.md`. **Report go/no-go.**
3. Then proceed phase by phase per Part III, honoring §20.3.

### 25. Acceptance of this spec

Implementation may begin. The first deliverable is the Phase 0 report: spike results, baselines, and the go/no-go recommendation on `jacobsa/fuse` under FUSE-T.
