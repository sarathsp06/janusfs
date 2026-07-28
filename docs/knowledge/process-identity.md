---
type: Design
title: Process identity
description: Identifying the calling process behind a FUSE operation, what it is actually for, and which parts of the obvious design are wrong.
tags: [identity, pid, security, macos, design]
status: partial
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: platform
    resource: platform-isolation.md
    title: why identity is only needed on macOS
  - id: node
    resource: /internal/mount/janus_node.go
    title: no caller inspection exists today
---

# Status

**Partial (PRP 06 Task 1 + Task 2 landed; Tasks 3 & 4 deferred pending PRP
07).** `internal/procid` exists: `Identity`, `MemRegistry`, the four-step
`IsAgent` resolution order (start-time-revalidated cache → environ scrape
→ ancestry walk → not-agent), and the darwin/linux platform seams
(`KERN_PROC_PID` + `KERN_PROCARGS2` on darwin, `/proc/<pid>/stat` +
`/proc/<pid>/environ` on linux) are implemented and tested off-mount.

Task 1's gate passed with wide margin: cache-hit revalidation is ~9 µs on
darwin/arm64 against a 250 µs per-op budget — recorded in
`bench/BASELINE.md`.

**Load-bearing caveat, discovered while implementing Task 2 on macOS
26.5.2**: `KERN_PROCARGS2` no longer returns the `envp` region for a
cross-process (even same-uid) read; the buffer stops after `argv`. The
"If this is wrong" section of PRP 06 named exactly this scenario as
report-not-ship-quietly. Consequence: on recent darwin, `environ()` returns
nil for another process, and `IsAgent`'s primary mechanism (step 2) is
effectively unavailable — the ancestry walk (step 3) becomes primary.
That is a materially weaker classifier: an agent subprocess that re-parents
(e.g. via `setsid` + PID-1 adoption) loses its filtered view. This is
recorded here rather than being papered over.

Tasks 3 (execrunner session registration with a barrier) and 4 (plumb
caller identity through `resolve()`/`decisionFor()`, gated on
path-preserving mode) are **deferred until PRP 07 (macOS path-preserving
mode) is written**: with no path-preserving mode to gate on yet, plumbing
identity through the adapter would either short-circuit unused or add
per-op cost with no beneficiary. See [PRPs/README.md](/PRPs/README.md).

# What it is for

Not "preventing PID recycling attacks". That framing inverts the actual
motivation.

Identity exists for exactly one purpose: to make a **macOS path-preserving
overmount safe for the user's own tools**. Once the sanitized view sits over
`~/projects/app`, every process on the machine reads through it, and showing
redacted bytes to `git add` destroys data (see
[platform isolation models](platform-isolation.md), Option B). Identity is what
lets one mount show a filtered face to the agent and the real face to
everything else.

It follows that:

- **Linux does not need this at all.** A private mount namespace makes the
  question meaningless — a process either sees the mount or does not, decided by
  the kernel.
- Identity is not a security boundary. It is a compatibility mechanism that
  happens to also enforce policy. Treating it as a boundary leads to the wrong
  failure behaviour, which is the subject of most of this document.

# The question to answer

For an incoming operation with caller PID `p`: **is `p` inside a registered
agent session?**

Not "is `p` the registered PID" — an agent spawns shells, `npm`, `node`, test
runners, language servers, and every one of them must inherit the filtered view.

# Mechanisms, in the order they should be tried

## 1. An inherited environment token (primary)

At registration, `janusfs exec` stamps a random token into the child's
environment as `JANUSFS_SESSION`. Environment is inherited across `fork`,
`setsid`, and `exec`, so every descendant carries it regardless of how the
process tree is reshaped.

Reading another process's environment without cgo:

- macOS: `sysctl` `KERN_PROCARGS2` for the target PID, which returns argv and
  environ for a process owned by the same uid. Reachable through
  `unix.SysctlRaw`.
- Linux: `/proc/<pid>/environ` (and unnecessary, per above).

This survives the reparenting that breaks ancestry walking, which is why it is
primary rather than a fallback.

Note the interaction with the existing env scrub: `execrunner.Run` strips all
`JANUSFS_*` variables from the child (`internal/execrunner/runner.go:186`)
precisely so the child cannot influence JanusFS. The session token is the one
deliberate exception, and it must be a per-session random value that grants
nothing on its own — it is an identifier, not a capability, and possession of it
gets a process *more* restriction, not less.

## 2. Ancestry walk (fallback), memoized

Walk the PPID chain from the caller upward until a registered session root is
found, or PID 1 is reached.

- macOS: one `KERN_PROC_PID` lookup yields a `kinfo_proc` carrying both the
  parent PID and the process start time.
- Linux: one `/proc/<pid>/stat` read yields field 4 (`ppid`) and field 22
  (`starttime`); the parser is a single-pass byte scanner anchored on the last
  `)` so process names containing spaces or parentheses cannot misalign fields.

The implementation exposes this as `parentAndStartTime(pid)` so the ancestry
walk reads each ancestor once instead of separately calling `parent(pid)` and
`startTime(pid)`. The walk is still O(depth) OS reads, which is far too expensive
per FUSE operation, so the verdict must be memoized.

## 3. Start time, for cache-key disambiguation only

A `(pid, startTime)` pair identifies a process uniquely for the lifetime of a
boot. That makes it the correct **cache key** for a memoized verdict: a recycled
PID has a different start time, so it cannot collide with a cached entry, and the
cache therefore needs no TTL for correctness.

That is the whole role of start time. It is a cache-correctness device, not a
defence against an attack.

# What the obvious design gets wrong

Four parts of the conventional "identity tuple" sketch are actively wrong, and
building them would cost effort and add failure modes for nothing.

**A PPID chain hash is the wrong primitive.** Hashing `"1042->812->1"` and
comparing digests answers "is this the exact chain I recorded", but the question
is "is this a descendant of a registered root", which the walk answers directly
and more cheaply. Worse, the hash is *fragile in the normal case*: when an
intermediate process exits, its children reparent to `launchd` (macOS) or `init`
(Linux), the chain changes, the digest stops matching, and a legitimate agent
subprocess silently loses its filtered view. Use the walk. Do not hash it.

**Boot UUID is unnecessary.** It guards against a PID and start time recorded
before a reboot matching a different process after one. The registry is in-memory
in the daemon, and the daemon does not survive a reboot, so there is nothing
stale to guard. Keep the registry in memory and the field is dead weight. Only
persist registrations if there is a demonstrated need, and only then add it.

**Process group and session ID are not usable as the primary key.** A subshell
or a backgrounded helper calling `setsid` detaches from both. They may be
recorded as corroborating evidence; they cannot be the match.

**"Fail closed to `EACCES` on lookup error" is the wrong direction, and the
original sketch contradicts itself about it** — its diagram says "fall back to
passthrough" while its prose says `EACCES`. The correct resolution follows from
what identity is for:

> An unidentifiable caller is treated as a **host process** and gets passthrough.

The reasoning: a registered agent's identity is inherent, not asserted. It cannot
lose its own identity by accident in a way that matters, but a host tool can
easily be unidentifiable — a short-lived process that exits between the operation
and the lookup, a `sysctl` failure under load, a process owned by another uid.
Returning `EACCES` there breaks the user's editor, their build, and Finder, for
no security gain, because the process getting the answer was never the agent.
Failing closed here fails the *wrong* thing closed.

The residual risk is stated in
[platform isolation models](platform-isolation.md): a deliberately evasive local
process reaches the unfiltered view. That risk is inherent to macOS, not
introduced by this choice, and the honest mitigation is that `janusfs exec` is
the only sanctioned way to launch an agent, so registration always happens.

# Shape of the implementation

A new `internal/procid` package, with no dependency on `mount` or `engine`, so it
is testable without a mount:

```go
// Identity is one process, uniquely identified for this boot.
type Identity struct {
    PID       int
    StartTime int64
}

// Registry answers whether a caller belongs to a registered agent session.
type Registry interface {
    Register(sessionToken string, root Identity)
    Unregister(sessionToken string)
    // IsAgent memoizes its verdict keyed by (pid, startTime).
    IsAgent(pid int) bool
}
```

Platform specifics go in `procid_darwin.go` and `procid_linux.go` behind one
internal interface: `parentAndStartTime(pid)`, `startTime(pid)`, `parent(pid)`,
`environ(pid)`. `startTime` and `parent` are compatibility wrappers around the
combined lookup. Both platform implementations are pure `golang.org/x/sys/unix`;
cgo remains forbidden.

The caller PID reaches the adapter through go-fuse's caller context, which the
adapter must plumb into the decision path — today `resolve()` takes no caller
argument (`internal/mount/janus_node.go:122`), so this is a signature change
through the adapter, not a local addition.

# The cost that decides whether this ships

Every FUSE operation would perform at least one process lookup to re-read the
caller's start time and confirm the memoized verdict. `NFR-3`'s budget is 250 µs
of added p99 latency for an allowed operation. The measured cache-hit
revalidation cost is comfortably under budget on darwin/arm64, and PR #12's
linux/amd64 measurements put `BenchmarkStartTime` at ~8.7 µs and a depth-5
ancestry walk at ~34.9 µs after the combined lookup/parser optimization.

If these measurements stop fitting the budget on a supported platform, the
honest conclusion is that macOS path-preserving mode does not ship there, and
the disjoint-mountpoint model stays the answer — which is a perfectly acceptable
outcome, because Linux gets real isolation either way.
