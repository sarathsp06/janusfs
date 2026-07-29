---
type: Architecture
title: Platform isolation models
description: Why Linux can enforce path parity with a private mount namespace and macOS cannot, and what each model can actually guarantee.
tags: [linux, namespaces, macos, macfuse, isolation, design]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: parity
    resource: exec-and-path-parity.md
    title: the path-parity problem
  - id: darwin
    resource: /internal/mount/mount_darwin.go
    title: macFUSE mount options
  - id: linux
    resource: /internal/mount/mount_linux.go
    title: Linux mount, no DirectMount today
  - id: node
    resource: /internal/mount/janus_node.go
    title: path-based backing access via LoopbackNode
---

# The asymmetry

Path parity means the sanitized view lives at the *same absolute path* as the
real source. Achieving it requires mounting over the source directory. What
happens next differs fundamentally between the two platforms, and that
difference determines almost every design decision in this area.

| | Linux | macOS |
|---|---|---|
| Per-process mount views | yes, `CLONE_NEWNS` | **no such mechanism** |
| Who sees the FUSE mount | only the agent's process tree | every process on the machine |
| Enforcement boundary | the kernel | a policy decision inside our daemon |
| Host tool overhead | zero — host tools never touch FUSE | every access routes through FUSE |
| Blast radius of a daemon crash | the namespace dies with the process tree | the whole project directory hangs |
| Can a determined local process evade it | no | yes, in principle |

Everything below follows from that table.

# Linux: private mount namespace

A process in a new mount namespace sees mounts that no other process sees. Mount
the sanitized view over `/Users/me/projects/app` inside that namespace and the
agent gets path parity, while `git`, the editor, and every other host tool
continue reading `ext4` at full native speed and never observe the FUSE mount at
all.

## Constraints that shape the implementation

**Unprivileged `CLONE_NEWNS` requires `CLONE_NEWUSER`.** Creating a mount
namespace needs `CAP_SYS_ADMIN`. Without root, the only way to get it is to
create a user namespace at the same time, where the process becomes uid 0 mapped
to the real uid. That has visible consequences: the process believes it is root,
which changes the behaviour of some tools, and a single uid/gid mapping is
enough (no `/etc/subuid` involvement).

**Go cannot `unshare` itself.** `unshare(CLONE_NEWNS)` affects only the calling
thread, and the Go runtime migrates goroutines across threads freely. The only
reliable mechanism in Go is `exec.Cmd` with
`SysProcAttr{Cloneflags: CLONE_NEWNS|CLONE_NEWUSER, UidMappings, GidMappings}`,
which applies the flags at `clone` time for the new process. So a
namespace-based `exec` must **re-exec the janusfs binary** as an intermediate
stage rather than unsharing in place.

**The mount must be made private first.** A new mount namespace inherits the
parent's mounts as *shared* by default on most distributions, which means a
mount created inside it propagates back out to the host. The first act inside
the namespace must be
`mount("", "/", "", MS_REC|MS_PRIVATE, "")`. Omitting this silently defeats
the entire isolation model — the host would see the FUSE mount appear over its
project directory.

**Who runs the FUSE server.** The server has to be reachable from inside the
namespace where the mount lives. Two options:

1. *The exec process is itself the server.* Simple, no cross-namespace
   plumbing, and the mount's lifetime is exactly the agent's lifetime, which is
   the correct semantics. Costs one server process per exec invocation, and the
   daemon has to be told about the mount over the socket if it is to appear on
   the dashboard.
2. *The daemon opens `/dev/fuse` and passes the fd to the child over
   `SCM_RIGHTS`, which calls `mount(2)` with `fd=N` in its own namespace.*
   Keeps one server and one owner.

Option 2 is what "stable descriptor handling" in the original design sketch was
reaching for, but go-fuse exposes no supported API for adopting an
externally-created `/dev/fuse` file descriptor, so it would mean hand-rolling
the `mount(2)` call with FUSE option strings plus fd passing — a large amount of
unsafe plumbing for no user-visible gain. **Option 1 is the design.**

**`DirectMount` becomes available.** Inside the user namespace the process holds
`CAP_SYS_ADMIN`, so go-fuse's `MountOptions.DirectMount` can mount without
shelling out to `fusermount`. `mount_linux.go` does not set it today
(`internal/mount/mount_linux.go:77`).

## As implemented (PRP 04)

Two re-exec stages, matching the constraints above:

- **Stage 1**, `internal/execrunner/runner_linux.go` (`//go:build linux`):
  discovers the source tree by walking up for `.janusfs.yml`
  (refusing, per [PRP 01](/PRPs/01-correctness-fixes.md)'s reasoning, rather
  than defaulting to cwd), then re-execs `os.Executable()` as
  `janusfs __nsmount --src <path> -- <command> [args...]` with
  `SysProcAttr{Cloneflags: CLONE_NEWNS|CLONE_NEWUSER, UidMappings, GidMappings,
  GidMappingsEnableSetgroups: false}`. No daemon dial happens on this path at
  all — daemon registration for dashboard visibility, mentioned as
  best-effort/optional in the PRP, was cut for this pass rather than risk
  touching the daemon's protocol for a purely cosmetic feature; `janusfs exec`
  working with no daemon running was the hard requirement, and skipping
  registration trivially satisfies it.
- **Stage 2**, `cmd/janusfs/nsmount_linux.go`'s hidden `__nsmount` command:
  runs only inside the fresh namespace. First act,
  unconditionally: `unix.Mount("", "/", "", MS_REC|MS_PRIVATE, "")`.

### Deviation from the original blueprint: a shadow bind mount, not a direct overmount

[PRP 04](/PRPs/04-linux-namespace-exec.md) named its single highest-risk
assumption explicitly: does `fs.LoopbackNode` (which the adapter still embeds;
[PRP 05](/PRPs/05-dirfd-backing-layer.md)'s descriptor-relative backing layer
doesn't exist yet) recurse into its own mount when the FUSE server is mounted
directly over its own backing source? The PRP's instruction was to spike this
empirically on real Linux hardware before deciding.

**That spike could not be run** — this implementation pass was authored on a
darwin-only development machine, where `CLONE_NEWNS`/`CLONE_NEWUSER` don't
exist even for testing. Given that constraint, `runNSMount` does not mount
directly over `src` as the PRP's pseudocode showed. Instead, before the FUSE
mount is established, it bind-mounts `src` to a private temporary shadow path
(`unix.Mount(src, shadow, "", MS_BIND, "")`) and backs the adapter by the
*shadow* path while mounting the FUSE server at the *real* `src`:
`adapter.Mount(ctx, shadow, src)`. A bind mount is the same file content at an
independent VFS path, so `.janusfs.yml` discovery and every
backing read the server performs go through `shadow`, never through `src` —
regardless of what ends up mounted at `src`, there is no path through which the
server's own backing I/O can re-enter its own mount.

This is a strictly safer default than the PRP's literal pseudocode, has no
known downside, and does not depend on PRP 05 landing first. It is also,
consequently, **unverified**: it has not been exercised against a real kernel.
`internal/execrunner/isolation_linux_test.go` and
`isolation_linux_bench_test.go` were written to the best ability of someone
who has read the relevant kernel documentation carefully, gated behind
`//go:build linux && fuseintegration`, and are ready to run — but they have not
actually been run. **Run them on a real Linux machine (`make integration`
under a Linux CI runner, or directly with FUSE available) before treating
either PRP 04 or this section as validated.** If the shadow-mount approach
turns out to be unnecessary (a direct `adapter.Mount(ctx, src, src)` proves
safe after all), it can be simplified later — that would be a welcome finding,
not a required one.

## What this buys

Kernel-enforced isolation. There is no identity check to spoof, no registry to
race, no PID to recycle: a process either is in the namespace or is not, and the
kernel decides. `internal/execrunner`'s path rewriting becomes dead code on
Linux. Host tool overhead is exactly zero, not "reduced".

# macOS: no per-process mount views

macOS has no mount namespace equivalent. A macFUSE mount is global. This gives
three genuinely different options, and the trade-off is not close.

## Option A: disjoint mountpoint (what ships today)

The view lives at a different path. Host tools never touch FUSE. Nothing can be
misclassified. The cost is the entire path-parity problem and the fragile
rewriting described in [exec and path parity](exec-and-path-parity.md).

## Option B: path-preserving overmount, one policy for everyone

Mount over `~/projects/app`. Path parity achieved. But now the *user's own
tools* read the sanitized view, and that is not merely inconvenient — it is a
data-loss hazard:

- `git add .` on a masked file stages a buffer of asterisks. A commit later, the
  real secret is gone and the repository contains `****`.
- `sed -i`, `prettier --write`, and every formatter read the redacted bytes and
  write them back — except that a write-intent open on a masked file returns
  `EACCES`, so instead they fail mid-operation, which for tools that truncate
  first can leave a file empty.
- Spotlight and Time Machine index and back up the sanitized bytes.

Option B is not viable as a default. It should not be offered without identity
enforcement.

## Option C: path-preserving overmount plus caller identity

Mount over the source, and use the calling process's identity to decide which
face to show: registered agent processes get the filtered view, everything else
passes through unfiltered. This is the only design that delivers path parity on
macOS without the hazards of Option B, and it is the reason
[process identity](process-identity.md) exists as a subsystem at all.

Its cost is honest and must be stated plainly: **enforcement moves from the
kernel to a heuristic in our daemon.** A local process that deliberately evades
identification — double-fork, `setsid`, scrub its own environment — reaches the
unfiltered view. Linux namespaces cannot be evaded; this can.

That is acceptable for the actual threat model, where the agent is an LLM that is
untrusted but not an adversary purpose-built to escape, and where a hostile local
process could simply read the source directory directly anyway. It is not
acceptable to describe it as equivalent to the Linux guarantee.

## The overmount recursion trap

If the FUSE server resolves backing files by path, and the mount covers that
same path, then every backing access re-enters the mount. Infinite recursion,
immediate deadlock.

The fix is that the server must hold a directory file descriptor to the real
directory, opened **before** the mount is established, and perform all backing
access relative to it with `openat`, `fstatat`, `readlinkat`, `unlinkat`,
`renameat`, and so on. On Linux the handle is `open(src, O_PATH|O_DIRECTORY)`;
macOS has no `O_PATH`, so `open(src, O_RDONLY|O_DIRECTORY)` serves as the
`openat` base.

This is not an optimisation. Path-preserving mode is impossible without it.

And it is expensive, because `fs.LoopbackNode` — which the current adapter
embeds and inherits almost everything from
(`internal/mount/janus_node.go:47`) — resolves backing files by joining the root
path with the node's relative path (`absPath()`, `:114`). A dirfd-relative
backend means replacing `LoopbackNode` with a hand-written backing layer.

The consolation is that the same change fixes a second problem independently
worth fixing: today every path-based backing access is a TOCTOU window, because
the path is re-resolved by the kernel after the policy decision was made. A
retained dirfd plus `openat` with `O_NOFOLLOW` closes it.

# Recommended sequencing

The two platforms have very different cost-to-value ratios, so they should not
be built together.

1. **Linux namespace exec.** Self-contained, no changes to the FUSE adapter, no
   identity subsystem, and it deletes code. Highest value per unit of risk.
   **Done** — [PRP 04](/PRPs/04-linux-namespace-exec.md), with the shadow-mount
   caveat above still needing verification on real Linux hardware.
2. **Crash recovery and the correctness fixes** in
   [known gaps](known-gaps.md). Cheap, and they benefit every platform. **Done**
   — [PRP 01](/PRPs/01-correctness-fixes.md), [PRP 02](/PRPs/02-crash-recovery-watchdog.md),
   [PRP 03](/PRPs/03-decision-cache.md).
3. **The dirfd backing layer.** Large, mechanical, independently valuable for
   TOCTOU, and a hard prerequisite for anything below it.
4. **Process identity**, verified against the real cost of a lookup per
   operation.
5. **macOS path-preserving mode**, off by default, refusing to enable until 3
   and 4 are in place.

Nothing in steps 3 to 5 should block shipping step 1.

# What was deliberately not adopted

Three ideas from the original design sketch were considered and rejected. They
are recorded here so they are not re-proposed:

- **A four-tier fast-path router with an "outside active scope" first tier.**
  A scoped mount has no out-of-scope operations: the kernel only sends the
  server operations for paths under the mountpoint. The tier is unreachable
  code. What remains after removing it — identity lookup, then policy
  resolution, with canonicalization only where the path can differ — is not a
  router, it is the existing call sequence with one step added.
- **Global `$HOME` overmounting on macOS.** Never do this. But note that the
  current implementation already mounts strictly per project source
  (`ResolveMountpoint`, `internal/config/config.go:345`), so "shift from a
  global overlay to scoped project roots" describes work that is already done.
  The remaining macOS work is parity, not scoping.
- **Per-caller kernel cache policy** — serving cached pages to host tools and
  uncached pages to agents from one mount. The FUSE page cache and dentry cache
  are keyed by inode in the kernel, with no notion of which process caused the
  fill. This is not implementable, and an implementation that appears to work is
  leaking. The sound answers are the ones already in place: `FOPEN_DIRECT_IO`
  on every masked handle (`internal/mount/janus_node.go:233`) and zero
  attribute and entry timeouts. Under Linux namespaces the question dissolves
  entirely, because host tools never enter FUSE.
