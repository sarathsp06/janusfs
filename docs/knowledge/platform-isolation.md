---
type: Architecture
title: Platform isolation models
description: Why Linux can enforce path parity with a private mount namespace and macOS cannot, and why the compensating macOS enforcement track was rejected.
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
known downside, and does not depend on PRP 05 landing first. It is now
**verified per-PR by the `fuseintegration` CI job**: Linux CI (ubuntu-latest)
runs `make integration` on every PR ([PRP 10](/PRPs/10-linux-ci-verification.md)),
which exercises `internal/execrunner/isolation_linux_test.go`,
`isolation_linux_bench_test.go`, and the overmount spike test
`internal/mount/overmount_linux_test.go` against a real kernel. If the
shadow-mount approach turns out to be unnecessary (a direct
`adapter.Mount(ctx, src, src)` proves safe after all), it can be simplified
later — that would be a welcome finding, not a required one.

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

## Option B: path-preserving overmount, one policy for everyone — rejected, see SPEC §20 and PRPs/12

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

Option B is not viable as a default, and it is no longer queued behind identity
enforcement either: the whole macOS enforcement track is rejected
([SPEC.md §20](/SPEC.md), [PRP 12](/PRPs/12-delete-macos-enforcement-track.md)).
The `git add` hazard it describes is real even for the shipping disjoint
mountpoint (a child committing *through* the filtered view), which is why
`janusfs check` and `janusfs exec` now warn about git-stageable Masked files
(`internal/check.GitStagingHazards`).

## Option C: path-preserving overmount plus caller identity — rejected, see SPEC §20 and PRPs/12

Mount over the source, and use the calling process's identity to decide which
face to show: registered agent processes get the filtered view, everything else
passes through unfiltered. This was the only design that could deliver path
parity on macOS without the hazards of Option B, and it was the reason a
process-identity subsystem (`internal/procid`, PRPs 06/07 — all deleted)
existed at all.

Its cost was honest and ultimately decisive: **enforcement moves from the
kernel to a heuristic in our daemon.** A local process that deliberately evades
identification — double-fork, `setsid`, scrub its own environment — reaches the
unfiltered view. Linux namespaces cannot be evaded; this could.

That heuristic ceiling is why the track was rejected rather than shipped:
kernel-shaped guarantees cannot be simulated by a daemon-side identity guess,
and describing the two as equivalent would have been false. The rejection is
recorded in [SPEC.md §20](/SPEC.md); the deletion is
[PRP 12](/PRPs/12-delete-macos-enforcement-track.md). macOS now ships Option A
only, advisory by design.

## Option D: Seatbelt confinement of the exec process tree (`--sandbox`, PRP 09) — rejected and deleted, see SPEC §20 and PRPs/12, PRPs/14

PRP 09 shipped, and PRP 14 later deleted, a `janusfs exec --sandbox` flag that
wrapped the already-disjoint-mounted child (Option A) in a `sandbox-exec`
(Seatbelt) profile denying kernel-level read/write of the real source subtree
while re-allowing the mountpoint. It was the one cgo-free macOS mechanism that
confined an entire subprocess tree, and it genuinely closed Option A's
direct-`open()` gap — verified end-to-end, including against a real path
reaching the child through an environment variable.

It was rejected with the rest of the macOS enforcement track
([SPEC.md §20](/SPEC.md), [PRP 14](/PRPs/14-delete-sandbox-flag.md)): Seatbelt
can only allow or deny, not rewrite, so it never addressed path parity; it
rested on `sandbox-exec`, which Apple has deprecated; and a lone opt-in flag
whose guarantee differed fundamentally from the Linux one invited exactly the
false-equivalence the table at the top warns against. `internal/execrunner/sandbox_darwin*.go`
is gone; macOS exec is advisory-only (cwd hijack + env scrub, argv verbatim).
The hard-won operational lessons (canonicalize deny targets including the APFS
firmlink twin; re-allow the mountpoint last) are preserved in
[PRP 09](/PRPs/09-macos-seatbelt-exec.md)'s findings for anyone re-proposing
kernel confinement on macOS.

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

This is not an optimisation. Any overmount is impossible without it — which is
why the Linux namespace exec bind-mounts a shadow path (above), and why the
rejected macOS path-preserving mode would have required it too.

[PRP 05](/PRPs/05-dirfd-backing-layer.md) landed exactly this for the read
path: `internal/backing` retains a directory descriptor for the source root
and the security-relevant reads go through `openat`-family calls with
`O_NOFOLLOW`, which also closed the read-path TOCTOU window (the path used to
be re-resolved by the kernel after the policy decision was made). The
remaining path-based mutation surface is tracked by
[PRP 16](/PRPs/16-prp05-ceiling.md).

# Sequencing, as it played out

The two platforms had very different cost-to-value ratios, and the 2026
repositioning resolved the split decisively toward Linux:

1. **Linux namespace exec.** **Done** —
   [PRP 04](/PRPs/04-linux-namespace-exec.md), with the shadow-mount design
   now verified per-PR by the `fuseintegration` CI job on real Linux
   ([PRP 10](/PRPs/10-linux-ci-verification.md)).
2. **Crash recovery and the correctness fixes** in
   [known gaps](known-gaps.md). **Done**
   — [PRP 01](/PRPs/01-correctness-fixes.md), [PRP 02](/PRPs/02-crash-recovery-watchdog.md),
   [PRP 03](/PRPs/03-decision-cache.md).
3. **The dirfd backing layer.** **Done** for the read path —
   [PRP 05](/PRPs/05-dirfd-backing-layer.md); its remaining ceiling is
   tracked by [PRP 16](/PRPs/16-prp05-ceiling.md).
4. **Process identity** and **macOS path-preserving mode** — **rejected, not
   pending**. The entire macOS enforcement track (procid, path-preserving
   overmount, `--sandbox`) was deleted; see [SPEC.md §20](/SPEC.md) and
   [PRP 12](/PRPs/12-delete-macos-enforcement-track.md). macOS remains
   Option A, advisory-only.

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
