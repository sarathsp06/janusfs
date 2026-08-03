# PRP 09 — macOS Seatbelt confinement for `janusfs exec`

## Goal

Give `janusfs exec` on macOS a real, kernel-enforced deny boundary via
`sandbox-exec` (Seatbelt), behind an opt-in `--sandbox` flag. Today the darwin
runner is advisory: it hijacks CWD, rewrites argv, and scrubs env, but the child
can still `open()` the real source at its own path. With `--sandbox`, the child
process tree is confined so that the real source subtree is unreadable and
unwritable, while the disjoint mountpoint (the filtered view) stays fully usable.

This closes the macOS advisory gap for **Hidden** (deny). **Masked** is unchanged:
Seatbelt can deny bytes but cannot rewrite them, so masking is still served by
FUSE through the mountpoint. The flag constrains the agent *to* the masked view;
it does not add masking.

## Why

macOS has no per-process mount namespace available to a third-party tool, so the
Linux mechanism (private mount namespace, `runner_linux.go`) cannot be ported.
Seatbelt is the one macOS primitive that confines an entire subprocess tree at
the kernel level without a signed system extension, and it is invokable as a
plain binary (`/usr/bin/sandbox-exec`) — no cgo, which the project forbids.

A feasibility spike (`docs/SEATBELT_SPIKE.md`) validated the mechanism on
Darwin 25.5: a `(deny file-read*)(deny file-write*)` profile over the canonical
source path denied a direct read, a child (`bash -c cat`), and a grandchild
(`find -exec cat`), denied writes to the source, allowed reads/writes through the
mountpoint, and refused a nested attempt to re-exec `sandbox-exec` with a
permissive profile. This PRP turns that spike into a shipped, flag-gated feature.

## Context

Read first (per `PRPs/README.md`):
1. `docs/knowledge/exec-and-path-parity.md` — how the darwin runner simulates
   path parity today.
2. `docs/knowledge/platform-isolation.md` — the Linux-vs-macOS enforcement split.
3. `docs/SEATBELT_SPIKE.md` — the validated profile, the two fail-open gotchas,
   and the open risks this PRP inherits.

Key anchors:
- `internal/execrunner/runner.go` (build tag `darwin`) — the runner this PRP
  extends. The injection point is where the child command is constructed:
  `cmd := exec.CommandContext(ctx, finalArgs[0], finalArgs[1:]...)` at
  `runner.go:182`. Everything before it (mount discovery, readiness poll, CWD
  hijack, argv rewrite, env scrub) is unchanged and still required — Seatbelt is
  **additive** to the disjoint-mount model, not a replacement for it.
- `cmd/janusfs/exec.go` — the cobra command. Note `DisableFlagParsing: true`
  (`exec.go:33`): cobra does not parse flags here, so `--sandbox` must be pulled
  out of the pre-`--` args by hand (see Task 3), not registered with
  `cmd.Flags()`.
- `runner_linux.go` — the Linux path. **Do not touch it.** On Linux the namespace
  already denies the real path for read and write; `--sandbox` is a no-op there
  and must be accepted-and-ignored, not an error (keeps the CLI uniform).

## Design

Additive to the existing darwin flow. When `--sandbox` is set:

1. **Resolve the deny target canonically.** The single most important correctness
   step, and a fail-**open** trap if skipped (spike gotcha 1). `src` from mount
   discovery is absolute but not canonical. Compute:
   - `canon = filepath.EvalSymlinks(src)` — collapses `/var`→`/private/var` and
     any symlinked ancestor.
   - the **firmlink twin**: if `canon` has prefix `/Users/`, `/Applications/`,
     `/Library/`, etc. (the APFS Data-volume firmlink roots), also deny
     `/System/Volumes/Data` + canon. If it already has that prefix, also deny the
     stripped form. Deny **both** forms; a firmlink reachable by the un-denied
     name is a silent bypass. (Spike open item: whether Seatbelt collapses
     firmlinks itself is unverified — denying both is the safe assumption.)

2. **Generate the profile** (pure function, unit-testable, no I/O):

   ```scheme
   (version 1)
   (allow default)
   (deny file-read*  (subpath "<canon>") (subpath "<firmlink-twin>"))
   (deny file-write* (subpath "<canon>") (subpath "<firmlink-twin>"))
   (deny file-read*  (subpath "<~/.janusfs>"))   ; cheap hardening, see Residual risk
   ```

   `(allow default)` first, denies last — order is last-match-wins (spike
   gotcha 2). The mountpoint is **not** mentioned: it lives outside `src`, so
   `(allow default)` already covers it. Do not add an explicit mount allow — it
   invites someone to "simplify" the source deny into a default-deny that breaks
   the toolchain.

3. **Wrap the command.** Prepend `/usr/bin/sandbox-exec -p <profile> --` to the
   already-rewritten `finalArgs`, i.e. build the child as
   `exec.CommandContext(ctx, sandboxExecPath, "-p", profile, "--", finalArgs...)`.
   Use `-p <profile>` (inline) not `-f <file>` — no temp file to create, secure,
   or clean up. `cmd.Dir`, `cmd.Env`, stdio, and signal forwarding are unchanged.

4. **Fail closed on a missing/using sandbox.** If `--sandbox` is set and
   `/usr/bin/sandbox-exec` is not executable, return a non-zero exit with a
   one-line cause — **never** silently run the child unsandboxed. A user who
   asked for confinement and didn't get it is the worst outcome (fail-open).

### Residual risk (document, don't over-solve in v1)

Under `(allow default)` the child keeps loopback networking and can reach the
daemon dashboard's `GET /api/v1/reveal` (`internal/api/server.go:107`), which
serves raw source bytes. This is **not** a practical bypass in v1: the bearer
token is in-memory only (`cmd/janusfs/runtime.go:87-92`) and not reachable by the
child. Denying loopback wholesale would break agents that need localhost (dev
servers, `npm install` against a local registry), so v1 leaves loopback allowed,
denies read of `~/.janusfs` as cheap defense-in-depth, and records the residual
in `docs/knowledge/known-gaps.md`. A future hardened profile can scope-deny the
daemon port.

## Tasks

**Task 1 — `internal/execrunner/sandbox_darwin.go` (new, build tag `darwin`).**
Pure helpers, no process launch:
- `func canonicalDenyTargets(src, home string) ([]string, error)` — EvalSymlinks
  + firmlink twin + `~/.janusfs`. Returns the subpaths to deny.
- `func sandboxProfile(denyTargets []string) string` — emits the profile text.
  Quote each subpath; reject any path containing `"` or a newline (defensive —
  such a path can't come from a real mount, but an unescaped one is a
  profile-injection / fail-open).
- `const sandboxExecPath = "/usr/bin/sandbox-exec"` and
  `func sandboxAvailable() error`.

**Task 2 — wire it into `runner.go`.** Thread a `sandbox bool` parameter through
`Run` (see Task 3 for how it arrives). At `runner.go:182`, when `sandbox` is
true: call `sandboxAvailable()` (fail closed on error), build `denyTargets`, and
construct the child as `sandbox-exec -p <profile> -- <finalArgs>` instead of
`finalArgs` directly. When false: unchanged.

**Task 3 — `cmd/janusfs/exec.go`.** Because `DisableFlagParsing` is on, scan the
args *before* `--` for `--sandbox` (and `--sandbox=false`), strip it, and pass a
bool into `execrunner.Run(ctx, targetArgs, sandbox)`. Update `Run`'s signature in
**both** `runner.go` and `runner_linux.go` (Linux accepts-and-ignores). Extend
the `Long` help: document `--sandbox` (macOS: kernel-enforced deny via Seatbelt;
Linux: no-op, the namespace already enforces), and add the uid-0 note the SPEC
requires in exec help.

**Task 4 — docs.** Update `docs/knowledge/platform-isolation.md` and
`exec-and-path-parity.md` with the as-built Seatbelt path; flip
`docs/SEATBELT_SPIKE.md` status to "implemented (flag-gated) in PRP 09"; add or
update the macOS residual-risk entry in `known-gaps.md`; append a line to
`docs/knowledge/log.md`.

## Validation

```bash
rtk make verify                    # fmt-check vet test-race — green before and after
rtk go test ./internal/execrunner/...   # unit: profile generation + canonicalization
rtk make integration               # darwin+fuseintegration: the real sandbox-exec test
```

**Unit (runs everywhere, no privileges):** table tests for `sandboxProfile` and
`canonicalDenyTargets` — assert last-match-wins ordering, both firmlink forms
present, `~/.janusfs` denied, and that a path containing `"` or newline is
rejected.

**Integration — `internal/execrunner/sandbox_darwin_integration_test.go`
(`//go:build darwin && fuseintegration`).** This is the test that matters, and
unlike PRP 04's Linux suite it **runs on the dev machine**. It reproduces the
spike against real `sandbox-exec` over two temp dirs (a "source" with a secret, a
"mount"), asserting, each as a subtest:
- direct read of the source secret → denied (exit ≠ 0, `Operation not permitted`)
- read through the mount → allowed
- child (`bash -c cat`) of the source → denied
- grandchild (`find … -exec cat`) of the source → denied
- write to the source path → denied, file byte-identical afterward
- write into the mount → allowed
- confined child re-execs `sandbox-exec -p '(allow default)'` over the source →
  denied (nested loosen refused)
- **positive path:** `git status` / a trivial build inside the mount still exits 0
  (guards against a profile that denies the source so broadly the toolchain
  breaks — the "usable dev environment" claim the spike left unverified)

`make leak-oracle` is not required (this PRP touches `execrunner/`, not
`mount/`, `redact/`, or `provider/`), but run it anyway since exec is a read
path into those.

## Done when

- `janusfs exec --sandbox -- <cmd>` on macOS confines the child tree: the real
  source is unreadable/unwritable, the mount is fully usable.
- `--sandbox` with no `/usr/bin/sandbox-exec` exits non-zero with a one-line
  cause (never runs unsandboxed).
- `--sandbox` on Linux is accepted and ignored.
- Without `--sandbox`, darwin behavior is byte-for-byte unchanged.
- Unit + darwin integration tests green; `make verify` green.

## If this is wrong

Load-bearing assumptions — stop and report if any is false:
- **`sandbox-exec` denies by canonical path and confines the whole subprocess
  tree.** Validated in the spike for CLI processes. **Not** validated for a
  signed/Electron harness (Cursor.app, the Claude Code app) — TCC/hardened-runtime
  may reject the wrapping sandbox. This PRP ships the flag for plain-CLI agents;
  wrapping an app bundle is out of scope and must be tested separately before
  being recommended.
- **The disjoint mount stays reachable while the source is denied.** The mount is
  a different path outside `src`, so `(allow default)` covers it — but if a future
  mount root is ever placed *under* `src`, the source deny would also kill the
  mount. Assert mount-path is not under `src` at runtime; error if it is.
- **`sandbox-exec` is not removed by Apple.** It is long-deprecated but present on
  Darwin 25.5. If a target macOS lacks it, `--sandbox` fails closed (correct), but
  the feature is unavailable there.

## Anti-patterns

- **Do not** invoke the `sandbox_init` C API — it needs cgo, forbidden here. The
  binary wrapper is the only sanctioned path.
- **Do not** run the child unsandboxed when `--sandbox` is set and the sandbox is
  unavailable. Fail closed.
- **Do not** convert the profile to `(deny default)(allow <mount>)`. Default-deny
  is stronger but breaks the toolchain (every dylib, temp dir, and `$HOME` read)
  and is a much larger validation surface. Ship default-allow + deny-source first.
- **Do not** rewrite `runner_linux.go` to add Seatbelt. Linux already enforces
  read+write via the namespace; a second mechanism is redundant and misleading.
- **Do not** cite this PRP or an `FR-` number in a code comment (per
  `PRPs/README.md`); state the constraint and the reason instead.

---

## Related / follow-on PRPs (scoped, not part of this branch)

These complete the enforcement story but ship independently (one PRP per branch):

- **PRP 10 — masked-write-back mitigation (`git add`→`****`).** SPEC FR-31a: `.git`
  is Allowed passthrough, so inside *any* enforced view (Linux namespace or a
  Seatbelt-confined tree) `git add` of a Masked file stages `****` into real git.
  Scope: route the agent's git to a scratch clone/worktree, **or** a `janusfs git`
  wrapper that skips masked paths on `add`/`commit`. SPEC now requires a chosen
  mitigation before enforcement is marketed as safe for agents with commit
  access, so this gates the *marketing* of PRP 09, not its code.

- **`janusfs check` tracked-and-masked warning** (small, fits in
  `internal/check/check.go`). Warn when a path resolving Masked or Hidden is also
  git-tracked (`git ls-files`), because that is exactly the file `git add` will
  poison. One `[warn]` line with the PRP-10 mitigation as the suggestion. Makes
  the FR-31a hazard visible at lint time instead of at data-loss time.
