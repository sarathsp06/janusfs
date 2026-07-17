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
- [ ] No new dependency outside SPEC §20.4's allowlist without a `docs/SPEC_AMENDMENTS.md` entry.
- [ ] Logging/error-handling conventions (§21) followed: no raw bytes logged, sentinel errors only, single `ToErrno` translation point untouched.

## Phase entries

### Phase 0
- No user data flows through the system yet (passthrough only, no rules). Primary risk: an incorrect FUSE adapter silently corrupting or losing data in `<src>` during the spike. Mitigation: spike run only against disposable fixture trees, never a real project directory.
