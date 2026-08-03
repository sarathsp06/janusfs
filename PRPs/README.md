# PRPs — Product Requirement Prompts

A PRP is a self-contained implementation blueprint: enough context, exact file
anchors, a task-ordered plan, and a runnable validation loop that an agent can
execute in one pass without re-deriving the architecture.

## Before executing any PRP

Read, in order:

1. [`docs/knowledge/architecture.md`](../docs/knowledge/architecture.md) — how the
   system fits together, with `file:line` anchors.
2. [`docs/knowledge/conventions.md`](../docs/knowledge/conventions.md) — the rules
   that get a change rejected.
3. The PRP itself.

Do **not** read `SPEC.md` cover to cover first. Each PRP links the sections it
needs.

## Execution protocol

```bash
rtk make verify          # must be green BEFORE you start; if not, stop and report
# ... implement the PRP's task list in order ...
rtk make verify          # must be green when you finish
rtk make leak-oracle     # must be green for any PRP touching mount/, redact/, provider/
```

Rules that apply to every PRP:

- **One PRP per branch.** Do not combine.
- **Never cite `SPEC.md` or an `FR-` number in a code comment.** Comments state
  the constraint or the reason; traceability runs docs → code, not back.
- If a PRP's assumption turns out to be false, **stop and report** rather than
  improvising around it. Every PRP has an "If this is wrong" section naming its
  load-bearing assumptions.
- When a PRP closes an item in
  [`docs/knowledge/known-gaps.md`](../docs/knowledge/known-gaps.md), delete that
  item from the register and append a line to
  [`docs/knowledge/log.md`](../docs/knowledge/log.md).

## Order and gating

Sequenced by value per unit of risk. Each is independently shippable; nothing
later blocks anything earlier.

| # | PRP | Blocked by | Size | Status |
|---|-----|-----------|------|--------|
| 01 | [Correctness fixes](01-correctness-fixes.md) | — | S | done |
| 02 | [Crash recovery watchdog](02-crash-recovery-watchdog.md) | — | S | done |
| 03 | [Decision cache](03-decision-cache.md) | — | S | done |
| 04 | [Linux namespace exec](04-linux-namespace-exec.md) | — | L | done, **unverified on real Linux** — see below |
| 05 | [Dirfd backing layer](05-dirfd-backing-layer.md) | — | L | read path done; mutations still via LoopbackNode |
| 06 | [Process identity](06-process-identity.md) | 05 | M | Tasks 1-2 done; 3-4 deferred pending 07 |
| 07 | [macOS path-preserving mode](07-macos-path-preserving.md) | 05, 06 | L | design captured; implementation gated on PRP 06 Tasks 3-4 |
| 08 | [Reload revocation of open handles](08-reload-revocation.md) | 03 | S | done |
| 09 | [macOS Seatbelt confinement for exec](09-macos-seatbelt-exec.md) | — | M | done — `--sandbox` flag, validated end-to-end against a real daemon/mount |

**PRP 04 was implemented on a darwin-only development machine**, where the
Linux-only mechanisms it depends on (`CLONE_NEWNS`/`CLONE_NEWUSER`,
`/proc/self/mountinfo`) don't exist even for testing. Every darwin-buildable
piece was verified (`make verify` green, cross-compiles clean for
`linux/amd64` and `linux/arm64`, `go vet` clean on both), and the
Linux-specific integration tests and benchmark
(`internal/execrunner/isolation_linux_test.go`,
`isolation_linux_bench_test.go`, both gated `linux && fuseintegration`) were
written and type-checked via cross-compilation, but **never executed**. The
single highest-risk assumption the PRP called out — whether mounting the FUSE
adapter directly over its own backing source deadlocks — could not be spiked
empirically, so the implementation took the strictly-safer path of a private
shadow bind mount instead (see
[platform-isolation.md](../docs/knowledge/platform-isolation.md)'s "As
implemented" section for the full reasoning). Run the isolation test suite on
a real Linux machine before treating PRP 04 as validated.

07 and 08 were deliberately unwritten until their prerequisites landed. 08
implemented after PRP 03's cache made per-read re-checks essentially free
(~55 ns cache-hit resolve). 07 written after PRP 06 Task 1 confirmed the
identity-lookup path fits inside NFR-3's budget; its implementation waits on
PRP 06 Tasks 3 and 4, which sequence more cleanly inside 07 than as a
separate branch (Task 4 explicitly gates on path-preserving mode existing).

## Requirement coverage

Mapping from the feature request to these PRPs, including the parts that are
already done or deliberately not being built.

| Request | Status | Where |
|---|---|---|
| 2.1 Linux VFS namespace engine | planned | [04](04-linux-namespace-exec.md) |
| 2.1 Stable descriptor handling (`O_PATH` dirfd) | planned | [05](05-dirfd-backing-layer.md) |
| 2.2 Scoped project mounts (macOS) | **already done** | `ResolveMountpoint`, `internal/config/config.go:345` — mounts are already per project source, never a global overlay |
| 2.2 Path parity on macOS | gated | 07, blocked on 05 + 06 |
| 2.2 Tier 1 fast-path (outside scope) | **not building** | Unreachable: the kernel only sends the server operations under the mountpoint. See [SPEC.md §20](../SPEC.md#20-risks-and-rejected-designs) |
| 2.2 Tier 2 (caller identity) | planned | [06](06-process-identity.md) |
| 2.2 Tier 3 (canonical resolution) | planned | [05](05-dirfd-backing-layer.md) |
| 2.2 Tier 4 (rule evaluation) | **already done** | `internal/rules/resolve.go:49` |
| 2.3 Identity: start time, memoized ancestry | planned | [06](06-process-identity.md) |
| 2.3 Identity: PPID chain hash | **not building** | Breaks on normal reparenting; answers the wrong question. [SPEC.md §11](../SPEC.md#11-process-identity) |
| 2.3 Identity: boot UUID | **not building** | Registry is in-memory and cannot outlive a reboot |
| 2.3 Fail-closed to `EACCES` on lookup error | **inverted, deliberately** | An unidentifiable caller is a host process and gets passthrough. The request contradicts itself here — its diagram says passthrough, its prose says `EACCES`. [SPEC.md NFR-2](../SPEC.md#4-non-functional-requirements) |
| 2.4 Symlink escape prevention | **already done** | `Readlink` + `escapesRoot`, `internal/mount/janus_node.go:428` |
| 2.4 Rule evaluation on canonical path | planned | [05](05-dirfd-backing-layer.md) |
| 2.4 Hardlink escape prevention | planned | [01](01-correctness-fixes.md) — this is the real bypass, and it is not in the request |
| 2.5 `FOPEN_DIRECT_IO` on masked reads | **already done** | `internal/mount/janus_node.go:233` |
| 2.5 Zero attr/entry timeouts | planned | [03](03-decision-cache.md) — make explicit what is currently accidental |
| 2.5 Per-caller cache policy | **not building** | Not implementable: FUSE caches are inode-keyed in the kernel with no notion of which process caused the fill |
| 2.5 Mutation matrix (write/rename/unlink/chmod) | **already done** | `internal/mount/janus_node.go:269`–`:420`; see [fuse-adapter.md](../docs/knowledge/fuse-adapter.md) for the as-built table |
| 2.6 Daemon watchdog | planned | [02](02-crash-recovery-watchdog.md) |
| 2.6 Separate `janus-watchdog` binary | **not building** | A hidden subcommand reuses the existing unmount ladder and registry loader; a second binary duplicates both |
| — | Case-folding glob evasion | [01](01-correctness-fixes.md) — found while reading; not in the request |

Three of the four "already done" rows matter for planning: roughly a third of the
original request describes work that shipped. The genuinely new surface is
Linux namespaces, the dirfd backing layer, identity, and the watchdog.
