# JanusFS Threat Model

Living document. Updated at every phase exit per SPEC.md §17 ("Security is a per-phase discipline, not a gate"). Each entry below should reference the SPEC section or FR/NFR it relates to.

## Trust boundary

- **Untrusted:** any process reading/writing through the FUSE mount (the AI agent, or anything else with access to `<mountpoint>`).
- **Trusted:** the human operating the `janusfs` CLI, the local machine's other processes (not sandboxed from each other), the localhost network stack (loopback only).
- **Boundary surfaces:** the mount point itself; the localhost HTTP/WebSocket API (`internal/api`); the on-disk history database (`internal/history`); log output (stderr).

## Assets

| Asset | Where it lives | Protected by |
|---|---|---|
| Secrets in Masked files (API keys, passwords, etc.) | Source tree, in-RAM redaction cache | Redaction (SPEC §8), fail-closed rebuild (FR-20) |
| Existence/content of Hidden files | Source tree | EACCES on open/read (FR-7, FR-8) |
| Access-pattern metadata (which paths read how often) | `internal/history` SQLite DB | Rollups-only content, `0600`/`0700` perms, retention pruning, `--no-history` (SPEC §3.8) |
| Dashboard API session | Bearer token minted per mount | Host/Origin validation, localhost-only bind (SPEC §11) |

## Leak channels (tracked; see SPEC §14 for the living summary)

| Channel | Status | Notes |
|---|---|---|
| Raw file bytes via mount read | Mitigated | FR-7 matrix; leak oracle (Phase 1+) |
| File size (Masked/Hidden files show real size) | Accepted trade-off | By design — avoids breaking tooling; size never reveals *content* |
| Timing (cache hit vs. rebuild is distinguishable) | Accepted trade-off | Documented; not considered exploitable for this threat model (local single-user tool) |
| Error text | Mitigated | Errors never embed file content (NFR-1); enforced by `internal/apperrors` sentinel-only design |
| Logs / events | Mitigated | Path + pattern name only, never matched bytes (NFR-1); lint rule blocks byte-slice args to `slog` in `redact`/`provider` |
| HTTP/WebSocket API | Mitigated (Phase 4 for full hardening) | Host header validation, Origin check, per-mount bearer token (SPEC §11) |
| History DB on disk | Accepted trade-off, mitigated | Deliberately persists path names + access patterns; rollups only, strict perms, pruning, opt-out (SPEC §3.8) |
| `.janusfs` virtual files | Mitigated | Read-only, never user-maskable/hidable (FR-27) |

## Phase-by-phase checklist template

Copy this into the phase's exit review:

- [ ] New leak channels introduced this phase identified and added to the table above.
- [ ] Fail-closed behavior verified for every new error path (does an internal error in this phase's code resolve to Hidden/EACCES, or could it leak?).
- [ ] Leak oracle (if it exists yet) extended with sentinels relevant to this phase's new masking/parsing surface.
- [ ] No new dependency outside SPEC §20.4's allowlist without a `SPEC.md` entry.
- [ ] Logging/error-handling conventions (§21) followed: no raw bytes logged, sentinel errors only, single `ToErrno` translation point untouched.

## Phase entries

### Phase 0
- No user data flows through the system yet (passthrough only, no rules). Primary risk: an incorrect FUSE adapter silently corrupting or losing data in `<src>` during the spike. Mitigation: spike run only against disposable fixture trees, never a real project directory.

### Phase 3 — Change detection (hot-reload, watcher)

**New components:** `internal/watch` (fsnotify-based recursive watcher), watcher wiring in `cmd/janusfs/mount.go` (debounce + recompiler goroutine).

**Security review:**

- **Watcher is advisory-only (FR-21).** The authoritative change detector is the per-read mtime/size/inode backstop in `internal/provider` (which existed before Phase 3). Even if the watcher misses an event, fails, or is not created (non-fatal), no unredacted bytes can leak — the backstop ensures cache staleness is detected independently. This is the key security property of the Phase 3 design.

- **Fail-closed on recompile error.** If `engine.Reload` fails (e.g., a newly written `.janusfs.yml` has a syntax error), `provider.InvalidateAll` is still called, so the cache is cleared of any content keyed to a stale rule set. The engine retains the *previous* valid rule set (FR-18: "the previous compiled generation serves until the new one is atomically swapped") — readers see the old rules, never an unredacted file that a new-but-broken rule would have masked.

- **Fail-closed on watcher overflow/error.** If `fsnotify` drops events (inotify/kqueue queue overflow) or returns an error from the Errors channel, the watcher signals the consumer via `onData("", 0)`, which triggers `provider.InvalidateAll()`. This is safe because: (a) the backstop re-validates every cache entry on subsequent reads anyway, and (b) full invalidation is conservative — it only causes a brief performance cost from cache miss, never a leak.

- **No file content passes through the watcher.** The watcher only receives file *paths* and event *types* (Create/Write/Remove/etc.) from fsnotify. It never opens, reads, or buffers file content. Logs from the watcher carry the config file path that changed (for debuggability) and the generation number after reload — never matched bytes or pattern-matched content (NFR-1).

- **Race between watcher event and concurrent read.** A data-file write triggers `provider.Invalidate(path)`. If a concurrent FUSE handler is mid-read on that path's cached entry, the invalidation deletes the entry from the cache map. The in-flight read's `waitAndServe` already has a reference to the entry's `bytes` slice — that goroutine finishes with whatever it had, and a subsequent read will miss the cache and trigger a rebuild. This is safe: the in-flight read served the *old* version, not unredacted bytes. The reloaded version is redacted with the same pattern set (or a new one, caught by the pattern-set-signature check in `waitAndServe`, which fails closed to EIO on mismatch).

**Checklist (Phase 3 exit):**

- [x] New leak channels introduced this phase: none. The watcher is advisory-only, never reads content, logs only paths and generations.
- [x] Fail-closed behavior verified for every new error path: recompile failure → old rules + full cache invalidate; watcher overflow/error → `InvalidateAll`; watcher creation failure → mount proceeds without hot-reload.
- [x] Leak oracle: not extended (Phase 3 adds no new masking/parsing surface; the oracle remains armed from Phase 1/2 for its existing sentinels).
- [x] No new dependency outside SPEC §20.4's allowlist: `fsnotify` is specifically allowed ("fsnotify" listed without asking in §20.4 and §9).
- [x] Logging/error-handling conventions (§21) followed: watcher logs use `internal/logging.New("watch")` (not bare `slog`), carry only paths and event metadata, never file content. `ToErrno` translation point is untouched (watcher doesn't produce errno values).
