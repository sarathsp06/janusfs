# PRP 13 — Delete the macOS argv path rewriter

**Size:** S · **Blocked by:** nothing · **Status:** done

## Why

`internal/execrunner/rewriter.go` rewrote absolute source paths in the child's
argv to the mountpoint. Its failure mode is structural: paths reach children
through env vars, config files, caches, and their own path discovery — no argv
rewrite can touch those. A shim that sometimes works is worse than a boundary
honestly described as advisory (SPEC §20 already rejects "improving" it: the
failure is structural).

## Change

- Delete `rewriter.go` + `rewriter_test.go`.
- `runner.go` (darwin): drop argv translation; keep the honest parts — cwd
  set to the sanitized mount, `JANUSFS_*` env scrub, readiness poll, signal
  forwarding, byte-faithful stdio.
- `runner_test.go`: assert argv passes through **verbatim** (the inverse of
  the old assertion).
- Package doc rewritten to state the advisory contract and why the rewriter
  was removed.

## Acceptance

`grep -r ReplacePaths internal/` is empty; darwin unit tests green; the mock
E2E test proves cwd hijack + env scrub still work and argv is untouched.
