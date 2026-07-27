# PRP 07 — macOS path-preserving mode

**Size** L · **Blocked by** [05](05-dirfd-backing-layer.md),
[06](06-process-identity.md) · **Touches** `internal/mount`,
`internal/execrunner`, `cmd/janusfs` · **Status** not yet implemented

## Goal

Let JanusFS mount its filtered view directly OVER the source directory on
macOS, so agent tools see the same file paths as the user's tools — no
`src` vs. `mountpoint` split, no argv rewriting, no CWD hijacking.

## Why

The current darwin `janusfs exec` (`internal/execrunner/runner.go`)
simulates path parity by rewriting arguments and stdout streams between
`src` and `mountpoint`. That works for well-behaved tools but breaks
whenever a tool computes a path from something other than argv or its own
output: filesystem-walking tools that stat by absolute path, symlink
chases into `src` from anywhere else on disk, editors that embed a path
in a lock file. On Linux, PRP 04's private mount namespace makes the
question go away — the kernel makes `src` mean the filtered view for the
agent and the real directory for everything else. This PRP is the macOS
answer to the same requirement.

## Prerequisites, both already landed

- **PRP 05 (dirfd backing layer)** — the FUSE server MUST reach the real
  backing bytes through a retained descriptor, because once the mount
  sits over `src`, resolving `src/foo` from the server re-enters the
  server's own mount and deadlocks. The read path is descriptor-relative
  as of PRP 05; the mutation path still delegates to `LoopbackNode`, so
  mutations under path-preserving mode need Task M2 below before this
  PRP can ship.
- **PRP 06 (process identity)** — every operation must distinguish the
  agent from the user's own tools; the user's `git add` on a masked file
  must stage plaintext, or one commit destroys data.

## Design sketch

**Activation.** Add a per-mount config flag `pathPreserving: true`
(`.janusfs/config` or `--path-preserving`). Off by default; the disjoint
`src` / `mountpoint` model stays the recommended shape.

**Mount site.** With the flag set, `internal/mount.Adapter.Mount` mounts
the FUSE view directly at `src` instead of at a chosen `mountpoint`.
`internal/backing` was designed exactly for this: `backing.Open(src)`
must be called BEFORE `fs.Mount(src, …)`, otherwise the retained
descriptor is against the newly-mounted-over path and every server-side
read re-enters the mount. This is why PRP 05's `Open` docstring says
"Must be called BEFORE any mount is established over dir."

**Per-operation gate.** `resolve()` and `decisionFor()` gain a caller
identity argument (PRP 06 Task 4). Where identity says HOST, the
adapter short-circuits to `Allowed` **without consulting policy at all**:

```
if pathPreserving && !registry.IsAgent(callerPID) {
    return engine.Resolution{Decision: engine.Allowed}
}
```

This is what keeps host tools at full speed — no rule tree walk, no
pattern match, no redaction bookkeeping. It is also what makes the mode
safe to leave enabled: with no known agent session, every process is a
host and the mount is effectively transparent.

**Failure direction.** An unidentifiable caller is a host, not an agent
(PRP 06 §"The failure direction, which is deliberately inverted"). Do
not fail closed here; failing closed breaks the user's editor and shell
for zero security gain.

**Mutation path.** The mutation operations that still delegate to
`LoopbackNode` (Unlink, Rename, Symlink, Link, Mkdir, Setattr's chmod,
Create, Mknod, and the write-side of Open) must be re-implemented on top
of `internal/backing` before path-preserving mode ships, because
LoopbackNode's path-based mutations re-enter the mount for the same
reason reads would. PRP 05 explicitly deferred this split to a follow-up;
it lands here.

**Exec integration.** With path-preserving mode on, `janusfs exec` on
darwin becomes trivial: no argv/stream rewriting is needed because the
child already sees `src` as the filtered view. `Run` becomes: register a
session with the daemon (PRP 06 Task 3), stamp `JANUSFS_SESSION` into
the child env, spawn with a barrier so registration finishes before the
child's first FS op, exec the target. The rewriter/CWD-hijack code
becomes dead in this mode and can be gated off.

## Tasks

1. **Config flag + activation** — `pathPreserving` in mount config;
   `internal/mount.Adapter.Mount` honours it by mounting at `src`.
   `newJanusRoot` still calls `backing.Open(src)` first; add an assertion
   that the fd was acquired before `fs.Mount` runs.
2. **Complete PRP 05's mutation split (Task M2)** — port
   Unlink/Rename/Symlink/Link/Mkdir/Rmdir/Setattr(chmod)/Create/Mknod off
   `LoopbackNode` onto `internal/backing`. Reads and directory streams
   were left to LoopbackNode in PRP 05; mutations must not be, because
   they re-enter the mount.
3. **PRP 06 Task 3** — session token generation, daemon registration,
   `JANUSFS_SESSION` env stamp with a barrier so the child cannot race
   its own registration.
4. **PRP 06 Task 4** — plumb caller identity through
   `resolve()`/`decisionFor()`; where identity says HOST **and**
   `pathPreserving` is on, short-circuit to `Allowed`. Report agent/host
   split through `internal/obs` and surface hit rate + mean lookup
   latency in `janusfs doctor`.
5. **Data-loss regression test** — path-preserving mode on; register a
   session; from the test process (host) read a `Masked` file and get
   plaintext; from a subprocess with `JANUSFS_SESSION=<token>` read the
   same file and get asterisks; `git add` of the masked file from the
   host must stage the real content.
6. **Simplify darwin exec** — with path-preserving mode active, gate off
   the argv/stream/CWD-hijack rewriter path in
   `internal/execrunner/runner.go` (darwin) and delete it once the
   disjoint model is fully retired. Keep it for now behind the flag —
   deleting it before path-preserving mode is validated on real macOS
   loses the fallback.

## Validation

```bash
rtk make verify
rtk make integration        # real mount over src
rtk make leak-oracle        # identity now decides who sees redaction
rtk make bench              # per-op identity lookup cost
```

Every test that exists today runs against the disjoint model. Add
parallel `pathPreserving=true` variants for the leak oracle and the
integration suite; they must pass with the same fixtures.

## Done when

- [ ] `pathPreserving: true` mounts the FUSE view at `src`; the retained
      descriptor was acquired before `fs.Mount` ran.
- [ ] All mutation operations that previously delegated to `LoopbackNode`
      now go through `internal/backing`.
- [ ] Caller identity gates the per-op decision; a host process gets
      `Allowed` short-circuit without a rule walk.
- [ ] Data-loss regression test passes: host `git add` on a masked file
      stages plaintext; agent read of the same file yields asterisks.
- [ ] `janusfs doctor` reports agent/host split and identity lookup
      latency.
- [ ] Existing disjoint-model tests still pass unchanged.
- [ ] `docs/knowledge/platform-isolation.md` gains an "As implemented
      (darwin path-preserving)" section; a line appended to `log.md`.

## If this is wrong

- **Assumes the descriptor acquired before `fs.Mount` remains valid when
  the mount lands on the same path.** The kernel replaces the path
  binding, not the underlying inode; the fd is against the inode, so it
  should stay valid. That is a strong assumption on darwin — spike it
  before task 1 is complete, and stop the PRP if the descriptor becomes
  useless once the mount is in place.
- Assumes per-op identity is affordable at NFR-3's budget. PRP 06
  Task 1's benchmark confirmed the cache-hit path is ~4% of the budget;
  the cold path (depth-5 ancestry walk) is ~18%. Both leave plenty of
  headroom, but a workload that constantly opens files by fresh PIDs
  will pay the cold cost per open.
- Assumes macOS 26+'s `KERN_PROCARGS2` restriction (which drops the
  environ region for cross-process reads — see PRP 06's Task 2
  writeup) does not silently degrade to the ancestry walk in a way that
  breaks reparented agent subprocesses. This is real risk on that OS;
  either accept the degradation or gate path-preserving mode off unless
  environ scraping works.

## Anti-patterns

- **Do not mount over `src` without descriptor-relative backing.** The
  server will re-enter its own mount on the first read. This is the
  overmount recursion trap described in `platform-isolation.md`; PRP 05
  exists to close it.
- **Do not treat identity as a security boundary.** It is a compatibility
  mechanism that also enforces policy; a determined local process that
  scrubs its own environment and detaches from its session reaches the
  unfiltered view. That is inherent to a platform with no per-process
  mount views, not a defect introduced by this PRP.
- **Do not delete the disjoint-model rewriter path before shipping.**
  Path-preserving mode is opt-in; the disjoint model stays the default
  until a full release cycle has proved the overmount is stable.
