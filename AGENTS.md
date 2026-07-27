# AGENTS.md — JanusFS

## First reads, in this order

1. **[`docs/knowledge/`](docs/knowledge/index.md)** — an OKF knowledge bundle
   describing the system *as built*, with `file:line` anchors. Read this instead
   of re-deriving the architecture from 8k lines of Go. Start at
   [`architecture.md`](docs/knowledge/architecture.md), then
   [`known-gaps.md`](docs/knowledge/known-gaps.md) for what is currently broken.
2. **[`SPEC.md`](SPEC.md)** — the binding contract: requirements, architecture
   constraints, delivery sequencing, and the rejected designs you should not
   re-propose.
3. **[`PRPs/`](PRPs/README.md)** — implementation blueprints for the work that is
   queued, in execution order, each self-contained. If you were asked to build
   something, check here first: it may already be planned in detail.
4. **[`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md)** — the living threat model.
5. **[`README.md`](README.md)** — the pitch, for humans.

If the bundle and the code disagree, that is a defect: fix the bundle in the same
change and append a line to its `log.md`.

## Quick commands

Prefix with `rtk` for token-reduced output.

| Action | Command | Notes |
|--------|---------|-------|
| Build | `rtk make build` | Output under `build/` |
| Run (dev) | `rtk make run ARGS="daemon"` | Needs FUSE installed locally |
| All tests | `rtk make test` | `go test ./...` |
| One package | `rtk go test -v ./internal/rules/...` | |
| Race | `rtk make test-race` | |
| Mounted integration | `rtk make integration` | Tag `fuseintegration`; mounts for real |
| Leak oracle | `rtk make leak-oracle` | Sentinel-secret scan over every mounted read |
| Benchmarks | `rtk make bench` | Compared against `bench/BASELINE.md` |
| Lint | `rtk make lint` | `golangci-lint` |
| Format | `rtk make fmt` | `gofmt` + `goimports -local github.com/sarathsp06/janusfs` |
| What CI runs | `rtk make verify` | `fmt-check vet test-race` |

## Architecture in six lines

- One long-lived `janusfs daemon` owns every mount. Mounts are structs inside it,
  not processes. Other subcommands are short-lived clients over
  `~/.janusfs/daemon.sock`.
- Core pipeline: `rules` compiles `.janusignore`/`.janusmask` → `engine` resolves
  a Decision behind an atomic snapshot → `provider` serves redacted bytes from a
  RAM cache → `mount` translates the Decision into a kernel answer.
- Three decisions, strict precedence: `HIDDEN > MASKED > ALLOWED`.
- There is **no file watcher**. Rule reload is explicit; the read-time cache key
  `(path, mtime, size, inode, generation)` is the authoritative change detector.
- Entrypoint `cmd/janusfs/main.go`, manual explicit dependency injection, no DI
  framework. `startMount` in `runtime.go` is the whole dependency graph.
- Both macOS (macFUSE) and Linux (FUSE) are supported. Their isolation models
  differ fundamentally — see
  [`platform-isolation.md`](docs/knowledge/platform-isolation.md).

## Packages

| Path | Purpose |
|------|---------|
| `cmd/janusfs/` | entrypoint, cobra commands, daemon, DI wiring |
| `internal/config/` | one `Config` struct; defaults, file/env overlay, `Validate` |
| `internal/logging/` | `slog` wrapper, one handler, per-component loggers |
| `internal/apperrors/` | sentinel errors and the single `ToErrno` translation |
| `internal/rules/` | discovery, parse, compile, resolve; in-house gitignore matcher |
| `internal/engine/` | atomic rule-set snapshot, generations |
| `internal/patterns/` | builtin and custom pattern library |
| `internal/redact/` | length-preserving redaction, streaming modes |
| `internal/provider/` | `RedactedContentProvider`: RAM cache keyed by `ContentKey` |
| `internal/mount/` | FUSE adapter — thin, no business logic |
| `internal/execrunner/` | `janusfs exec` orchestration |
| `internal/control/` | daemon control-socket protocol types and dial helper, shared by `cmd/janusfs` and `internal/execrunner` |
| `internal/obs/` | event bus, metrics, ring buffer, top-N |
| `internal/history/` | SQLite `Store` — rollups, sessions, coverage |
| `internal/health/` | diagnostics for `doctor` |
| `internal/check/` | static config linter, shared by `check` and `conflicts.json` |
| `internal/api/` | HTTP server — thin adapter |
| `internal/ui/` | embedded dashboard assets (`embed.FS`) |
| `internal/vfsmeta/` | `.janusfs` virtual file contents |

`internal/watch` and `internal/platform` do not exist and are not planned.
`internal/backing`, `internal/procid`, and `internal/nsexec` are planned but not
yet written — see [SPEC.md §18](SPEC.md#18-sequencing).

## Code conventions

The full list with reasons is in
[`conventions.md`](docs/knowledge/conventions.md). The rules that get changes
rejected:

- **Fail closed is the tiebreak.** Any ambiguity resolves to the option where the
  agent behind the mount sees *less*.
- **Never log file content.** Not a byte slice from `redact`, not one from
  `provider`, not at debug level.
- **Never cite SPEC.md from a code comment.** No `FR-`/`NFR-` numbers, no `§`
  references, no amendment dates. A comment states the constraint or the reason
  the code cannot show for itself. Traceability runs from the docs to the code,
  not back.
- **No errno construction outside `apperrors.ToErrno`**, called only from
  `internal/mount`.
- **No SQL outside `internal/history`'s `Store` methods.**
- **No flag or env reads outside `internal/config` and `cmd/janusfs`.**
- **No logging outside `logging.New(component)`.**
- **No cgo**, which is why history uses `modernc.org/sqlite` and process
  inspection must use `golang.org/x/sys/unix`.
- **Thin transport layers**: `internal/mount` and `internal/api` translate and
  delegate; they do not decide, redact, or query.
- Deliberate shortcuts with a known ceiling get a `ponytail:` comment naming the
  ceiling and the upgrade path.

## Test nuances

- Unit tests need no external services and no mount. History uses `:memory:`.
- Mounted integration tests need real FUSE and are behind the `fuseintegration`
  tag, so they are not part of `make test`.
- The **leak oracle** is a standing tripwire: sentinel secrets in `testdata/`
  must never appear in any byte read through a mount, in any test, ever. A
  failure blocks the change.
- Benchmark regressions past a performance budget block the change rather than
  warning.

## Working with SPEC.md

- Sequencing in [§18](SPEC.md#18-sequencing) is deliberate: each step is
  independently shippable, and nothing later blocks anything earlier.
- The interfaces in §7, §8, §9, and §11 are normative. Change them through a
  decision record, not incidentally.
- Need a behaviour the spec does not define? Implement the fail-closed reading,
  write a decision record, and flag it. Do not invent silently.
- Check [§20](SPEC.md#20-risks-and-rejected-designs) before proposing an
  isolation or identity design — several plausible ones are already rejected
  there with reasons.

## Release

- GoReleaser (`.goreleaser.yml`) builds and packages releases; universal binary
  on darwin, tarballs, sha256 checksums, and a changelog grouped by Conventional
  Commits.
- `.github/workflows/release.yml` runs on any `vX.Y.Z` tag and creates **draft**
  releases, so nothing publishes without a human reading the notes.
- Conventional Commit prefixes (`feat:`, `fix:`) are required, not preferred:
  they drive that changelog grouping.
- `make release-snapshot` builds locally without a tag; `make release-check`
  lints the GoReleaser config.
