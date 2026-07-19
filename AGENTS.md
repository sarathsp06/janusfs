# AGENTS.md — JanusFS

## First reads

- `SPEC.md` is the binding engineering contract — requirements (FR/NFR numbers), architecture, phased plan, and **Part IV: operating instructions for the coding agent**. Read it before writing code; every behavior must trace to an FR/NFR.
- `PLAN.md` is the product-level plan (why, not how) — read it for context, not for requirements.
- `README.md` explains the name and the pitch, for humans.
- `docs/THREAT_MODEL.md` is the living threat model — check it before assuming a behavior isn't specified.

## Quick commands

| Action | Command | Notes |
|--------|---------|-------|
| Build | `make build` | Output: `build/janusfs-darwin-$(ARCH)` |
| Run (dev) | `make run ARGS="mount <src> <mountpoint>"` | Needs macFUSE installed locally |
| Run all tests | `make test` | `go test ./...` |
| Single package test | `go test -v ./internal/rules/...` | |
| Race tests | `make test-race` | `go test -race ./...` |
| Mounted integration tests | `make integration` | `-tags fuseintegration`; needs macFUSE, mounts for real |
| Leak oracle | `make leak-oracle` | Sentinel-secret scan over every mounted read (Phase 1+) |
| Benchmarks | `make bench` | Compares against `bench/BASELINE.md`; regressions beyond NFR-3 fail |
| Lint | `make lint` | `golangci-lint` |
| Format | `make fmt` | `gofmt -w .` + `goimports -local github.com/sarathsp06/janusfs` |

## Architecture (see SPEC.md Part II for full detail)

- **Entrypoint**: `cmd/janusfs/main.go` — manual, explicit dependency injection (SPEC §15), no DI framework.
- **Mount**: `hanwen/go-fuse/v2` over macFUSE (macOS only, requires macFUSE kernel extension).
- **Core pipeline**: `rules` (compile `.janusignore`/`.janusmask`) → `engine` (resolve Decision) → `provider` (serve redacted bytes) → `mount` (FUSE adapter, thin). `watch` invalidates on change; never authoritative (SPEC §9, FR-21).
- **Observability**: `obs` (event bus, `JanusMetrics`, ring buffer) feeds both the live dashboard and `internal/history` (SQLite rollups — paths/counts only, never content, SPEC §3.8/§16).
- **Config**: flags via `internal/config`, single `Config` struct, `Validate()` before anything mounts.

## Key packages

| Path | Purpose |
|------|---------|
| `cmd/janusfs/` | Entrypoint + manual DI wiring |
| `internal/config/` | Flags/env → validated `Config` |
| `internal/logging/` | `slog` wrapper, per-component loggers |
| `internal/apperrors/` | Canonical error sentinels + `ToErrno` (single translation point) |
| `internal/rules/` | `.janusignore`/`.janusmask` discovery, parse, compile, generation |
| `internal/engine/` | Pure decision resolution (Hidden/Masked/Allowed) |
| `internal/patterns/` | Built-in + custom regex pattern library |
| `internal/redact/` | Streaming, size-preserving `*`-redaction |
| `internal/provider/` | `RedactedContentProvider`: RAM cache (MVP), tmpfs shadow (later) |
| `internal/watch/` | fsevents watcher, advisory only |
| `internal/mount/` | FUSE adapter (thin — no business logic) |
| `internal/obs/` | Event bus, `JanusMetrics`, ring buffer, top-N |
| `internal/history/` | SQLite `Store` — rollups, sessions, coverage snapshots |
| `internal/health/` | Liveness probe for `/healthz` and `janusfs doctor` |
| `internal/api/` | HTTP/WebSocket server (thin adapter over engine/provider/history) |
| `internal/ui/` | Embedded dashboard assets (`embed.FS`) |
| `internal/vfsmeta/` | `.janusfs` virtual files (`conflicts.json`, `status.json`) |
| `internal/check/` | Static config linter (shared by `check` and `conflicts.json`) |
| `internal/platform/` | OS seam (darwin now, linux later) |

## Code conventions

- **Fail-closed is the tiebreak.** Any ambiguity resolves to the option where the agent behind the mount sees *less*. See SPEC §20.2.
- **No SQL outside `internal/history`'s `Store` methods.**
- **No errno construction outside `internal/apperrors.ToErrno`**, called only from `internal/mount`.
- **No flag/env reads outside `internal/config`** and `cmd/janusfs`.
- **No logging outside `internal/logging.New(component)`.** Never log file content (byte slices from `redact`/`provider`) — this is a hard review-blocking rule (NFR-1).
- **Thin transport layers**: `internal/mount` and `internal/api` translate and delegate; they don't decide, redact, or query.
- **Naming**: packages lowercase single word, files `snake_case.go`, exported symbols reference their FR in a doc comment.
- **Dependency policy** (SPEC §20.4): `hanwen/go-fuse/v2`, `fsnotify`, `modernc.org/sqlite`, `prometheus/client_golang`, one WebSocket lib, stdlib — allowed without asking. Anything else needs a decision record first. Never: cgo (except the approved FUSE fallback), network-calling libs, telemetry SDKs.

## Test nuances

- Unit tests need no external services — SQLite history uses `:memory:`, the engine/provider/watcher are testable without a mount (NFR-8).
- Mounted integration tests (`-tags fuseintegration`) need macFUSE installed and actually mount — not run by default `make test`.
- The **leak oracle** is a standing tripwire from Phase 1 onward: sentinel secrets planted in `testdata/` must never appear in any byte read through the mount, under any test, ever.
- Benchmarks are compared against `bench/BASELINE.md` (captured in Phase 0); a regression past an NFR-3 budget blocks the task, not just a warning.

## Working with SPEC.md

- Phase order is strict (SPEC §20.3) — don't start Phase N+1 before Phase N's exit criteria (including its security and UX checklist items) are met.
- If you need a behavior the spec doesn't define: implement the fail-closed interpretation, add a decision record, and flag it — don't invent silently and don't block waiting for a human unless it's one of the stop-conditions in SPEC §23.
- The interfaces in SPEC §7–§9 (`Engine`, `RedactedContentProvider`, `Watcher`) are normative — change them only via a decision record, not incidentally while implementing something else.

## Release

- **GoReleaser** (`.goreleaser.yml`) builds and packages releases: darwin amd64 + arm64, combined into a universal binary (NFR-7), tarball archives, sha256 checksums, and a changelog grouped by Conventional Commits (`feat:`/`fix:`/other).
- `make release-snapshot` builds a local snapshot release (no tag/publish needed) to sanity-check the config; `make release-check` lints `.goreleaser.yml` itself.
- `.github/workflows/release.yml` runs GoReleaser on `git push --tags` for any `vX.Y.Z` tag, on a macOS runner (NFR-7: this is a macOS-only binary). Releases are created as **drafts** — nothing publishes without a human reviewing the generated notes first.
- Conventional Commits (`feat:`, `fix:`, …) are what drives the changelog grouping above — not just a style preference.
