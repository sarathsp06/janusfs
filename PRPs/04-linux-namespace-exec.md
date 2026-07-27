# PRP 04 — Linux private-namespace exec

**Size** L · **Blocked by** nothing · **Touches** new `internal/nsexec`,
`internal/execrunner`, `cmd/janusfs`, `internal/mount`, `internal/health`

## Goal

On Linux, `janusfs exec -- <cmd>` gives the child process tree a filtered view of
the project **at the project's own absolute path**, while every process outside
that tree keeps reading the real filesystem directly and never enters FUSE.

Then delete the path-rewriting machinery on Linux, because it exists only to
simulate what this now provides for real.

## Why

The mountpoint is never the source path: `ResolveMountpoint` mirrors the source's
full path *under* the mount root and `Validate` rejects any overlap
(`internal/config/config.go:345`, `:418`). So `janusfs exec` today compensates by
rewriting strings — the cwd, argv, and stdout/stderr
(`internal/execrunner/runner.go:162`, `:180`, `:201`).

That cannot be made correct. The rewriter reaches argv, stdout, and stderr. It
cannot reach: files the agent writes (a generated `tsconfig.json`, a lockfile, a
`compile_commands.json`), the git index, `cargo`/`go`/`tsc` build caches keyed on
absolute paths, debug paths baked into artefacts, or anything the agent spawns
that talks over a socket. The full analysis is in
[`exec-and-path-parity.md`](../docs/knowledge/exec-and-path-parity.md).

A private mount namespace solves it at the kernel level, and as a bonus gives the
strongest guarantee in the project: isolation that cannot be evaded, because a
process either is in the namespace or is not, and the kernel decides — no identity
heuristic, no registry to race, no PID to recycle.

## Context

- The problem: [`exec-and-path-parity.md`](../docs/knowledge/exec-and-path-parity.md)
- Platform trade-offs and why the daemon does *not* own this mount:
  [`platform-isolation.md`](../docs/knowledge/platform-isolation.md)
- Requirements: [SPEC.md FR-28, FR-29](../SPEC.md#35-isolation-and-path-parity),
  [NFR-9, NFR-11](../SPEC.md#4-non-functional-requirements)

Verified API facts:

- `fuse.MountOptions.DirectMount bool`, `DirectMountStrict bool`,
  `DirectMountFlags uintptr` all exist (`fuse/api.go:304`–`321`).
  `DirectMountStrict` makes go-fuse call `mount(2)` itself with no `fusermount`
  fallback — which is what we want inside the namespace, because a silent fallback
  to `fusermount` would mount in the *host* namespace.
- `fs.Options` embeds `fuse.MountOptions` (`fs/api.go:764`).

## Four constraints that shape the whole design

Each of these has a failure mode that is silent or confusing if you get it wrong.
They are the reason this is an L and not an M.

**1. Go cannot `unshare` itself.** `unshare(CLONE_NEWNS)` affects only the calling
thread and the Go runtime migrates goroutines across threads freely, so there is
no safe moment to call it in a running Go program. The only reliable mechanism is
`exec.Cmd` with `SysProcAttr.Cloneflags`, which applies at `clone` time to a fresh
process. **Therefore `exec` must re-exec the janusfs binary** as an intermediate
stage. This is not a workaround; it is the supported path.

**2. Unprivileged `CLONE_NEWNS` requires `CLONE_NEWUSER`.** Creating a mount
namespace needs `CAP_SYS_ADMIN`, and without root the only route is a user
namespace where the process becomes uid 0 mapped to the real uid. Consequence to
document in help text: the child believes it is root, and a few tools behave
differently as a result.

**3. The mount tree must be made recursively private first.** A new namespace
inherits the parent's mounts as *shared* on most distributions, so a mount created
inside it **propagates back to the host** and appears over the user's real project
directory. This is the single most damaging thing that can go wrong here, it is
silent, and NFR-9's test exists specifically to catch it:

```go
unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
```

**4. The exec process serves its own mount.** Not the daemon. go-fuse exposes no
supported API for adopting an externally created `/dev/fuse` descriptor, so having
the daemon own the mount would mean hand-rolling `mount(2)` with FUSE option
strings plus `SCM_RIGHTS` descriptor passing — a large amount of unsafe plumbing
for no user-visible gain. Serving in-process also makes the mount's lifetime
exactly the agent session's lifetime, which is the correct semantics.

Consequence: **`janusfs exec` must work with no daemon running.** Daemon
registration becomes best-effort, for dashboard visibility only.

## Blueprint

### Task 1 — Preflight: `internal/nsexec/support_linux.go`

A capability check that fails with a remedy instead of a confusing `EPERM` deep
inside a mount call.

```go
// Supported reports whether this kernel and configuration can host an
// unprivileged private-namespace mount, and if not, why, in terms a user can act
// on.
func Supported() error
```

Check, in order:

- `runtime.GOOS == "linux"`.
- Unprivileged user namespaces are enabled. Debian and some hardened kernels ship
  `kernel.unprivileged_userns_clone=0`; read
  `/proc/sys/kernel/unprivileged_userns_clone` when it exists and require `1`.
- `/proc/sys/user/max_user_namespaces` > 0.
- `/dev/fuse` exists and is openable.
- Kernel ≥ 4.18, which is when FUSE became mountable inside a user namespace
  (`FS_USERNS_MOUNT`). Below that, direct mount in the namespace cannot work.

Wire this into `janusfs doctor` so the answer is discoverable before someone hits
it mid-task.

### Task 2 — Stage 1: the launcher

**File** `internal/execrunner/runner_linux.go` (new), guarded by `//go:build linux`

Restructure `Run` so the Linux path diverges before any mount provisioning:

1. `nsexec.Supported()` — on failure, print cause and remedy and exit 125.
2. Resolve the source tree by walking up for `.janusignore`/`.janusmask`. Reuse
   the discovery half of `findSourceAndMount` (`runner.go:61`) but **not** the
   daemon-mount half. Per PRP 01 task 3, refuse rather than defaulting to cwd.
3. Best-effort: notify the daemon that a session started, for the dashboard.
   A dial failure is not an error here.
4. Re-exec self:

```go
cmd := exec.CommandContext(ctx, self, append([]string{"__nsmount", "--src", src, "--"}, target...)...)
cmd.SysProcAttr = &syscall.SysProcAttr{
	Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
	UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
	GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	GidMappingsEnableSetgroups: false,   // required for an unprivileged map
}
cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr   // no rewriting
cmd.Dir = cwd                                                        // no hijack
```

Note what is absent: no `StreamRewriter`, no `ReplacePaths` on argv, no cwd
hijack, no readiness poll on `<mountpoint>/.janusfs`. Paths are identical on both
sides, so there is nothing to translate.

5. Keep the existing signal forwarding (`runner.go:212`) and exit-code propagation
   (`runner.go:244`) unchanged. They are correct and platform-independent.

### Task 3 — Stage 2: `cmd/janusfs/nsmount_linux.go`

A hidden command that only ever runs inside the new namespace.

```go
func newNSMountCmd() *cobra.Command  // Use: "__nsmount", Hidden: true
```

Order matters in `runNSMount`, and step 1 must be first:

```
1. unix.Mount("", "/", "", MS_REC|MS_PRIVATE, "")     ← before anything else
2. eng  := engine.New(src)
   prov := provider.NewRamCache(...)                   from config defaults
3. adapter.Mount(ctx, src, src)                        ← mount OVER the source
4. wait for OnMounted
5. run the target command: cmd.Dir = original cwd, stdio inherited
6. cmd.Wait()
7. server.Unmount(), then exit with the child's code
```

Step 3 is the whole point: `src == mountpoint`. Two things must change to allow
it.

**`config.Validate` currently forbids it.** `pathsOverlap` rejects
`src == mountpoint` (`internal/config/config.go:418`). Add a mode flag to
`Config` — `PathPreserving bool` — and skip *only* the overlap and empty-directory
checks when set. Keep every other check. Do not delete the rules; a future macOS
mode needs the same relaxation and the same guard.

**The backing path must not resolve through the mount.** Once mounted over `src`,
resolving `src/foo` re-enters the mount and deadlocks. In this PRP the mount is
established *after* `engine.New(src)` has compiled the rule tree, so policy is
fine — but the adapter's backing reads go through `LoopbackNode`, which joins the
root path per access (`janus_node.go:114`).

**This is the load-bearing risk.** go-fuse's `LoopbackRoot` opens the root
directory when the server starts, and whether its subsequent access re-enters the
new mount is a property of go-fuse's implementation, not something to assume.
Verify it empirically as the *first* thing you build — a 30-line spike that mounts
a loopback over its own source and reads one file:

- **If it works**, proceed, and add a comment at the mount site recording that
  the ordering (server started before the mount is visible) is what makes it safe.
- **If it deadlocks**, stop and ship PRP 05 first, then return. Do not try to
  work around it with path tricks; the dirfd backing layer is the real fix and
  it is already planned.

Set direct mount on the Linux adapter (`internal/mount/mount_linux.go:77`):

```go
// Inside a user namespace we hold CAP_SYS_ADMIN, so mount(2) directly. Strict,
// with no fusermount fallback: fusermount would mount in the host namespace,
// silently defeating the isolation this exists to provide.
opts.DirectMountStrict = true
```

Guard it so it applies only on the namespace path, not to daemon-owned mounts,
which have no such capability.

### Task 4 — Delete the rewriter on Linux

Once Task 3 works: `ReplacePaths` and `StreamRewriter`
(`internal/execrunner/rewriter.go`) are unreachable from the Linux path. Move them
behind `//go:build darwin` along with the cwd-hijack and argv-rewrite blocks.

Do not keep them "just in case" on Linux. Two disagreeing sources of truth about
where a file lives is worse than either alone.

## Validation

```bash
rtk make verify
```

### The NFR-9 test is the one that matters

It must assert the **negative** — that the host cannot see the mount. A test that
only checks the child sees redacted content passes even when the mount has
propagated to the host, which is the exact bug it needs to catch.

`internal/nsexec/isolation_linux_test.go`, tagged `fuseintegration`:

```go
// 1. temp source tree with a secret file and a .janusmask masking it
// 2. snapshot /proc/self/mountinfo
// 3. run `janusfs exec -- cat <src>/secret.env`, capture stdout
// 4. assert stdout is redacted                      (the child sees policy)
// 5. read <src>/secret.env directly from the test process
// 6. assert it is the real content                  (the host does NOT)
// 7. assert /proc/self/mountinfo is byte-identical to the snapshot
//                                                   (nothing propagated)
```

Step 7 is the assertion that would have caught a missing `MS_REC|MS_PRIVATE`.

Also test:

- **simultaneity**: host and in-namespace reads of the same path, concurrently,
  return different content — proving the two views coexist rather than one
  replacing the other.
- **no daemon required**: kill the daemon, run `janusfs exec`, assert success.
- **teardown**: after exec exits, `/proc/self/mountinfo` is unchanged and the
  source directory is readable normally.
- **exit codes and signals**: `exec -- sh -c 'exit 42'` yields 42;
  `SIGINT` reaches the child.
- **unsupported kernel**: with `Supported()` stubbed to fail, the error names the
  reason and the remedy.

### NFR-11: zero host overhead

```bash
rtk make bench    # host-path read benchmarks, with and without an active exec session
```

The target is *zero* measurable regression, not "reduced" — host tools are not
touching FUSE at all, so anything else means something is wrong.

## Done when

- [ ] `janusfs exec -- cat secret.env` prints redacted content while the same path
      read from outside prints real content, concurrently
- [ ] `/proc/self/mountinfo` byte-identical before, during, and after
- [ ] `janusfs exec` works with no daemon running
- [ ] `nsexec.Supported()` failures produce a cause and a remedy, surfaced by
      `janusfs doctor`
- [ ] The rewriter, cwd hijack, and argv rewriting are `darwin`-only
- [ ] Host-path benchmarks show no regression
- [ ] `internal/nsexec` has no dependency on `internal/mount` internals beyond the
      public `Adapter`

## If this is wrong

Two assumptions can sink this, and both are cheap to test first — do that before
writing the rest:

1. **`LoopbackNode` does not re-enter the mount when mounted over its own
   source.** Spike it in 30 lines. If it deadlocks, do PRP 05 first.
2. **`DirectMountStrict` succeeds inside an unprivileged user namespace on the
   target kernel.** If it fails with `EPERM`, the kernel does not permit FUSE in a
   userns and `Supported()` must reject it up front rather than failing late.

## Anti-patterns

- Do not `unshare` in-process. It will appear to work and fail
  non-deterministically under load.
- Do not skip `MS_REC|MS_PRIVATE`. It is silent and it leaks the mount to the host.
- Do not use `DirectMount` without `Strict`. A `fusermount` fallback mounts in the
  host namespace.
- Do not pass a `/dev/fuse` descriptor from the daemon over `SCM_RIGHTS`. Rejected
  in [SPEC.md §20](../SPEC.md#20-risks-and-rejected-designs) with reasons.
- Do not make `exec` require the daemon.
- Do not add identity checks here. The kernel is the boundary on this path; that
  is the entire advantage.
