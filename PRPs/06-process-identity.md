# PRP 06 — Process identity

**Size** M · **Blocked by** [05](05-dirfd-backing-layer.md) · **Touches** new
`internal/procid`, `internal/mount`, `internal/execrunner`, `cmd/janusfs`

## Goal

Determine, per FUSE operation, whether the calling process belongs to a registered
agent session — cheaply enough to sit on the hot path.

**Task 1 is a benchmark with the authority to cancel this PRP.** Do it first, in
its own commit, and read the result before writing anything else.

## Why

Not "preventing PID recycling attacks". That inverts the motivation.

Identity exists for one purpose: to make a macOS path-preserving overmount safe
for the user's own tools. Once the filtered view sits over `~/projects/app`, every
process on the machine reads through it, and showing redacted bytes to `git add`
destroys data — one commit later the real secret is gone and the repository
contains `****`. Identity is what lets one mount show a filtered face to the agent
and the real face to everything else.

Two consequences follow, and both are easy to get backwards:

- **Linux does not need this.** A private mount namespace
  ([PRP 04](04-linux-namespace-exec.md)) makes the question meaningless: a process
  either sees the mount or does not, decided by the kernel. Build this for darwin.
  The Linux implementation exists only so tests can run everywhere.
- **This is not a security boundary.** It is a compatibility mechanism that also
  enforces policy. Treating it as a boundary produces the wrong failure behaviour,
  which is the subject of most of this document.

## Context

- Full design reasoning, including the rejected mechanisms:
  [`process-identity.md`](../docs/knowledge/process-identity.md)
- Why the boundary differs by platform:
  [`platform-isolation.md`](../docs/knowledge/platform-isolation.md)
- Requirements: [SPEC.md FR-32](../SPEC.md#35-isolation-and-path-parity),
  [NFR-2, NFR-10](../SPEC.md#4-non-functional-requirements),
  [§11](../SPEC.md#11-process-identity)

Verified API facts:

- `fuse.FromContext(ctx) (*fuse.Caller, bool)` (`fuse/context.go:45`).
  `fuse.Caller` embeds `Owner` (uid, gid) and carries `Pid uint32`
  (`fuse/types.go:658`). This is how the caller PID reaches a handler.
- Today `resolve()` (`internal/mount/janus_node.go:122`) takes no caller argument,
  so plumbing identity through is a signature change across the adapter, not a
  local addition.

---

## Task 1 — Benchmark first. This gates everything else.

Write `internal/procid/bench_test.go` before the implementation:

```go
func BenchmarkStartTime(b *testing.B)      // one sysctl / one /proc read
func BenchmarkIsAgentCacheHit(b *testing.B) // memoized verdict + start-time revalidation
func BenchmarkAncestryWalk(b *testing.B)    // cold path, depth 5
```

You need only `startTime(pid)` and `parent(pid)` to run these — roughly 60 lines.

The budget is 250 µs of added p99 latency for an allowed operation, and identity
must fit inside a fraction of it, because the operation still has to do its actual
work. A `KERN_PROC_PID` sysctl is *plausibly* a few microseconds. That is an
assumption, not a measurement.

**If the cache-hit path does not fit comfortably:**

Stop. Record the number in `bench/BASELINE.md`, write up the finding, and close
this PRP as declined. The honest conclusion is that macOS path-preserving mode does
not ship and the disjoint-mountpoint model remains the macOS answer. That is an
acceptable outcome — Linux gets kernel-enforced isolation either way — and it is a
far better result than shipping a mount that is 10× slower on every `stat`.

Do not attempt to rescue the number by weakening the check, caching the verdict
without revalidating start time, or sampling every N operations. Each of those
trades a correctness property for latency, and the property is the reason the
mechanism exists.

---

## Task 2 — `internal/procid`

Only if Task 1 passes. No dependency on `internal/mount` or `internal/engine`.

```go
// Identity is one process, unique for the lifetime of a boot. The start time is
// what makes the pair unique: a recycled PID has a different start time, so a
// (PID, StartTime) pair can never be confused with an earlier process.
type Identity struct {
	PID       int
	StartTime int64
}

// Registry answers whether a caller belongs to a registered agent session.
// Implementations must be safe for concurrent use from FUSE handlers.
type Registry interface {
	Register(sessionToken string, root Identity)
	Unregister(sessionToken string)
	IsAgent(pid int) bool
}
```

The registry is **in memory only**. It cannot outlive a reboot, which is exactly
why no boot UUID is needed (see Anti-patterns).

Platform seam, three functions, `golang.org/x/sys/unix` only, no cgo:

```go
// procid_darwin.go — unix.SysctlRaw with KERN_PROC_PID for startTime and parent;
//                    KERN_PROCARGS2 for environ.
// procid_linux.go  — /proc/<pid>/stat field 4 (ppid) and field 22 (starttime);
//                    /proc/<pid>/environ.
func startTime(pid int) (int64, error)
func parent(pid int) (int, error)
func environ(pid int) ([]string, error)
```

Parsing `/proc/<pid>/stat` has a trap worth stating: field 2 is the executable
name **in parentheses and may itself contain spaces or parentheses**. Split on the
last `)` in the line and index fields from there, never by naive whitespace
splitting from the start.

### `IsAgent` resolution order

```
1. cached verdict for (pid, startTime)?
     re-read startTime (one syscall) and compare against the cached pair.
     match   → return the cached verdict
     mismatch → the PID was recycled; purge and continue
2. environ(pid) contains JANUSFS_SESSION=<token> for a registered session?
     → agent. This is PRIMARY because it survives fork, setsid, and reparenting.
3. walk parent(pid) up to a registered session root or PID 1
     → agent if a registered root is found
4. anything else, including any error at any step
     → NOT an agent  (see the direction note below)
   memoize the verdict under (pid, startTime)
```

Memoization keyed on `(pid, startTime)` needs **no TTL for correctness**: a
recycled PID has a different start time, so it cannot collide with a cached entry.

### The failure direction, which is deliberately inverted

**An unidentifiable caller is treated as a host process and gets passthrough.**

The feature request contradicts itself here — its diagram says fall back to
passthrough, its prose says fail closed with `EACCES`. Passthrough is correct, and
the reasoning is not "convenience":

A registered agent's identity is *inherent*: it is carried in inherited
environment and process ancestry, and it cannot be lost by accident in a way that
matters. A host tool, by contrast, is easily unidentifiable — a short-lived process
that exits between the operation and the lookup, a `sysctl` failing under load, a
process owned by another uid. Returning `EACCES` there breaks the user's editor,
their build, and Finder, for no security gain, because the process being denied was
never the agent.

Failing closed here fails the *wrong thing* closed. This is the one documented
inversion of the project's fail-closed tiebreak, and
[SPEC.md NFR-2](../SPEC.md#4-non-functional-requirements) records it as such.

The residual risk is real and must not be papered over: a local process that
deliberately evades identification — double-fork, `setsid`, scrub its own
environment — reaches the unfiltered view. That is inherent to a platform with no
per-process mount views, not introduced by this choice, and it is why
path-preserving mode is opt-in and why `janusfs exec` is the only sanctioned way to
launch an agent.

---

## Task 3 — Session registration

**File** `internal/execrunner/runner.go`

On the darwin path, `exec` generates a `crypto/rand` token, registers
`(token, Identity{child pid, child start time})` with the daemon, and stamps
`JANUSFS_SESSION=<token>` into the child's environment.

This interacts with the existing env scrub (`runner.go:186`), which strips all
`JANUSFS_*` variables so the child cannot influence JanusFS. The session token is
the **one deliberate exception**, and it is safe because possession of it grants
nothing — it is an identifier, not a capability, and holding it gets a process
*more* restriction, not less. Comment it at the scrub site, or someone will
correctly delete it as a hole.

Registration must happen **before** the child can issue a filesystem operation,
otherwise its first reads race the registry and get passthrough. Register after
`cmd.Start()` (the PID is needed) but hold the child at a barrier until the daemon
confirms — a pipe the child reads one byte from before `exec`ing the real target is
the standard shape. Unregister on exit, including on signal paths.

## Task 4 — Plumb the caller into the decision path

`resolve()` and `decisionFor()` gain a caller argument. Where identity says
"host", short-circuit to `Allowed` **without consulting policy at all** — this is
the fast path, and it is what keeps host tools at full speed.

Gate the whole thing on path-preserving mode being active. In the default disjoint
model there is nothing to distinguish: only processes deliberately reading the
mountpoint see the view, so identity would add cost for no benefit.

Report through the existing observation channel so the dashboard can show the
agent/host split, and surface hit rate and mean lookup latency in
`janusfs doctor`.

## Validation

```bash
rtk make verify
rtk make bench       # Task 1's numbers, recorded in bench/BASELINE.md
rtk make leak-oracle # identity now decides who sees redaction
```

Classification tests, `internal/procid/`, which need no mount:

- direct child of a registered root → agent
- grandchild → agent
- after `setsid` → agent, via the environment token
- after a double-fork reparented to PID 1 → agent, via the environment token
- unrelated process → **not** an agent
- **recycled PID**: register an identity, let the process exit, fabricate a
  registry entry with a stale start time, assert the verdict is not inherited from
  the cache
- every error path — `startTime` fails, `environ` fails, PID gone — returns
  not-an-agent rather than an error to the caller

Integration, tagged `fuseintegration`, path-preserving mode on: a registered agent
reads a masked file and gets asterisks; the test process itself reads the same path
concurrently and gets plaintext.

Explicitly test the data-loss scenario, because it is the reason all of this
exists: a host-context `git add` on a masked file must stage the **real** content.

## Done when

- [ ] Task 1's benchmark ran and is recorded in `bench/BASELINE.md`, with either a
      pass or a written decline
- [ ] `internal/procid` is cgo-free and testable without a mount
- [ ] `(pid, startTime)` memoization, with a recycled-PID test proving no stale
      verdict is inherited
- [ ] Unidentifiable callers get passthrough, asserted, with the reason in a
      comment
- [ ] The `JANUSFS_SESSION` exception is documented at the env-scrub site
- [ ] Registration cannot be raced by the child's first filesystem operation
- [ ] `janusfs doctor` reports hit rate and mean lookup latency
- [ ] `/proc/<pid>/stat` parsing handles an executable name containing spaces and
      parentheses, with a test
- [ ] [`process-identity.md`](../docs/knowledge/process-identity.md) status changed
      from `draft` to `stable`, or the PRP's decline recorded there; a line
      appended to [`log.md`](../docs/knowledge/log.md)

## If this is wrong

- **The whole PRP is conditional on Task 1.** Treat a failing benchmark as the
  expected outcome, not a problem to engineer around.
- Assumes `KERN_PROCARGS2` can read the environment of a same-uid process without
  elevated privileges. If it cannot, mechanism 2 is unavailable, ancestry walking
  becomes primary, and reparenting then silently drops legitimate agent
  subprocesses to passthrough. That is a materially weaker design — report it
  rather than shipping it quietly.

## Anti-patterns

- **No PPID chain hash.** Hashing `"1042->812->1"` answers "is this the exact
  chain I recorded", not "is this a descendant of a registered root". It is also
  fragile in the *normal* case: when an intermediate process exits, children
  reparent to `launchd`, the chain changes, the digest stops matching, and a
  legitimate agent subprocess silently loses its filtered view. Walk the chain.
- **No boot UUID.** It guards a *persisted* registry against a reboot. This
  registry is in memory.
- **Do not use process group or session ID as the match key.** A subshell or
  backgrounded helper calling `setsid` detaches from both.
- **Do not return `EACCES` on a lookup failure.** See Task 2.
- **Do not skip start-time revalidation on a cache hit** to save a syscall. That
  is the one thing making PID reuse safe.
- Do not add identity to the Linux namespace path. The kernel is the boundary
  there.
