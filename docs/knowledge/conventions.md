---
type: Reference
title: Conventions
description: The house rules a change must follow, plus how to build, test, and release.
tags: [conventions, testing, build, style]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: agents
    resource: /AGENTS.md
    title: AGENTS.md conventions section
  - id: makefile
    resource: /Makefile
    title: build and test targets
  - id: apperrors
    resource: /internal/apperrors/apperrors.go
    title: sentinel errors and ToErrno
  - id: logging
    resource: /internal/logging/logging.go
    title: slog wrapper
---

# The tiebreak

**Fail closed.** Any ambiguity resolves to the option where the agent behind the
mount sees *less*. This is not advice; it is why `resolve()` recovers a panic
into `Hidden` (`internal/mount/janus_node.go:122`), why a config line that fails
to compile poisons its whole level (`internal/rules/rules.go:228`), and why
`ToErrno`'s default arm is `EIO` rather than something more specific
(`internal/apperrors/apperrors.go:58`).

# Hard rules

Each of these exists because breaking it has a specific consequence, so the
reason is stated rather than just the rule.

- **No errno construction outside `apperrors.ToErrno`, called only from
  `internal/mount`.** Keeps the error-to-errno mapping auditable in one function
  instead of scattered across handlers where a wrong errno leaks a wrong cause.
- **No SQL outside `internal/history`'s `Store` methods.** The history DB is the
  only thing that persists, and it is the one place path names touch disk.
- **No flag or env reads outside `internal/config` and `cmd/janusfs`.** Every
  tunable is a field on one `Config` struct, so `janusfs paths` and the config
  summary can be complete.
- **No logging outside `logging.New(component)`.** One JSON handler, configured
  once by `cmd/janusfs` via `SetOutput` (`internal/logging/logging.go:32`);
  never `slog.Default()`.
- **Never log file content.** Not a byte slice from `redact`, not one from
  `provider`, not in a debug log. This is review-blocking. Events and logs carry
  paths and pattern names only.
- **Thin transport layers.** `internal/mount` and `internal/api` translate and
  delegate. They do not decide, redact, or query.
- **No cgo.** Which is why history uses `modernc.org/sqlite` and why any process
  inspection must go through `golang.org/x/sys/unix`.

# Style

- Packages are lowercase single words; files are `snake_case.go`.
- Platform variants use build tags in separate files: `mount_darwin.go` /
  `mount_linux.go`, `doctor_darwin.go` / `doctor_other.go`,
  `stat_darwin.go` / `stat_linux.go`. Note that `janus_node.go` and
  `janus_virtual.go` carry `//go:build darwin || linux` since they are shared.
- **Code comments never cite SPEC.md.** No `FR-`/`NFR-` numbers, no `§`
  references, no amendment dates. A comment states the constraint or the reason
  the code cannot show for itself; a document coordinate is provenance, and it
  rots as soon as requirements are renumbered. Traceability runs the other way:
  [SPEC.md](/SPEC.md) and this bundle point at code.
- A deliberate shortcut with a known ceiling gets a `ponytail:` comment naming
  the ceiling and the upgrade path. Two exist today, both in
  `internal/provider/provider.go` (the single cache mutex at `:91`, and
  oversize re-redaction from byte 0 at `:220`).
- Dependency policy: `hanwen/go-fuse/v2`, `modernc.org/sqlite`,
  `prometheus/client_golang`, `spf13/cobra`, `jmoiron/sqlx`, `google/uuid`, and
  stdlib are in. Anything else needs a decision record first. Never: cgo,
  network-calling libraries, telemetry SDKs.

# Errors

Wrap with context at the call site using `%w` and let it propagate. The CLI
prints exactly one line: `main` sets `SilenceErrors`/`SilenceUsage` and formats
`janusfs: <cause>` itself (`cmd/janusfs/main.go:34`).

`cmd/janusfs/errors.go` holds the shared sentinels and repeated guidance
strings, and its own doc comment explains the admission criterion: text earns a
place there only if it is matched with `errors.Is` by more than one caller, or is
guidance repeated in more than one place. Per-call-site errors keep their context
where they are.

User-facing messages are written for an operator — no package prefix, no wrapped
syscall text. `config.Validate` is the model (`internal/config/config.go:373`).
Every failure prints a cause and a remedy.

# Build and test

| Action | Command |
|---|---|
| Build | `make build` |
| Run all tests | `make test` |
| Race | `make test-race` |
| One package | `go test -v ./internal/rules/...` |
| Mounted integration | `make integration` (tag `fuseintegration`; mounts for real) |
| Leak oracle | `make leak-oracle` |
| Benchmarks | `make bench`, compared against `bench/BASELINE.md` |
| Lint | `make lint` |
| Format | `make fmt` |
| Everything CI runs | `make verify` (`fmt-check vet test-race`) |

Per the project's own instruction, prefix shell commands with `rtk` for
token-reduced output: `rtk make test`, `rtk git status`.

# Testing rules

- The whole engine — rules, masking, cache, events — is testable **without a
  mount**. Keep it that way; it is the reason the test suite is fast and it is a
  stated non-functional requirement.
- Unit tests need no external services. History uses `:memory:`.
- Mounted integration tests are behind the `fuseintegration` tag and are not
  part of `make test`, because they need macFUSE and mount for real.
- **The leak oracle is a tripwire, not a test.** Sentinel secrets in
  `testdata/` must never appear in any byte read through a mount, in any test,
  ever. A failure there blocks the change.
- Benchmark regressions past an NFR-3 budget block the change too, rather than
  warning.
- Non-trivial logic leaves one runnable check behind. Test files sit beside
  their subject: `internal/rules/rules_test.go`, `glob_test.go`,
  `internal/mount/leak_oracle_test.go`, `integration_test.go`,
  `janus_virtual_unit_test.go`.

# Release

GoReleaser (`.goreleaser.yml`) builds darwin `amd64` and `arm64`, combines them
into a universal binary, and produces tarballs, checksums, and a changelog
grouped by Conventional Commits. `.github/workflows/release.yml` runs on any
`vX.Y.Z` tag and creates **draft** releases, so nothing publishes without a
human reading the notes.

Conventional Commit prefixes (`feat:`, `fix:`) drive that grouping, which is why
they are required rather than preferred.

`make release-snapshot` builds locally without a tag; `make release-check` lints
the GoReleaser config.
