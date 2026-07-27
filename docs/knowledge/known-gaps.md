---
type: Register
title: Known gaps
description: Defects and risks found by reading the working tree, ranked by severity, each with the exploit or failure and the intended fix.
tags: [defects, risks, security, correctness]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: node
    resource: /internal/mount/janus_node.go
    title: adapter overrides
  - id: glob
    resource: /internal/rules/glob.go
    title: gitignore matcher
  - id: runner
    resource: /internal/execrunner/runner.go
    title: exec source discovery
  - id: daemon
    resource: /cmd/janusfs/daemon.go
    title: daemon lifecycle
---

Every item below was found by reading source, not by running an exploit. The
two marked **unverified** need a test to confirm before they are treated as
fact; the rest are readable directly from the code.

Eight items closed so far — the agent hardlink bypass, the case-folding
bypass, `exec`'s silent cwd default, the duplicated control-protocol types,
`doctor`'s unrecoverable mountpoint ([PRP 01](/PRPs/01-correctness-fixes.md)),
the ungracefully-killed-daemon hang ([PRP 02](/PRPs/02-crash-recovery-watchdog.md)),
the unmemoized decision engine ([PRP 03](/PRPs/03-decision-cache.md)), and the
read-path TOCTOU window ([PRP 05](/PRPs/05-dirfd-backing-layer.md)) — have
been removed from this register. See those PRPs and [`log.md`](log.md) for
what changed.

# 1. A policy reload does not revoke an already-open handle

A file open and `Allowed` at open time keeps a passthrough handle. If a reload
makes it `Masked` or `Hidden`, reads through the existing handle continue to hit
the real file, because only `maskedHandle` re-resolves; a passthrough handle is
go-fuse's `LoopbackFile` with no interception.

The window is small and requires a reload during an open handle's lifetime, but
"tighten the policy" is exactly when the user expects the tightening to apply.

**Fix**: either wrap passthrough reads to re-check the decision (which now costs
only a cache-hit resolve per read on the fast path, since [PRP 03](/PRPs/03-decision-cache.md)
memoized `Resolve`), or on reload use go-fuse's inode notification to
invalidate affected entries and force reopen. The second is the better trade if
the notification path proves reliable.

Not yet scheduled — see [PRPs/README.md](/PRPs/README.md)'s note on why this
and macOS path-preserving mode are written only once their prerequisites land.

# 2. Dev-only mock paths are hardcoded to another machine

`runDaemon` honours `JANUSFS_MOCK_DEV=1` by starting two mock mounts at
`/home/jules/.janusfs/mounts/mock-project-*` (`cmd/janusfs/daemon.go:162`).
Harmless — it is behind an env var and only reachable deliberately — but the
paths belong to someone else's machine and the block bypasses `resume()`
entirely.

**Fix**: derive the paths from `os.UserHomeDir()`, or delete the block and use a
test fixture.

# 3. Unverified: xattr as a redaction side channel

`Getxattr` and `Listxattr` pass through for `Masked` files
(`internal/mount/janus_node.go:460`, `:474`). On macOS, extended attributes can
hold substantial data, and a resource fork (`com.apple.ResourceFork`) or a
`com.apple.metadata:*` attribute could in principle carry content that the
redaction pipeline — which only processes the data fork — never sees.

**Unverified**: needs a test that writes secret material into an xattr on a
masked file and reads it back through the mount. If it comes back plaintext,
the xattr row of the operation matrix needs to change for masked files.

# 4. Unverified: whether the `readdir` inode-zeroing has a cost

`Getattr` zeroes `out.Ino` on every call (`internal/mount/janus_node.go:209`) so
go-fuse assigns synthetic inode numbers, avoiding "overriding ino" warnings when
a file is replaced by `git checkout` or an editor's rename-on-save. The comment
explains the motivation clearly.

**Unverified**: whether synthetic numbering breaks anything that relies on
stable inode identity across a remount — `find -samefile`, hardlink detection in
`tar`/`rsync`, or `du` deduplication. Worth one test before treating it as free.

# 5. `TestVirtualDir` fails on at least one real macFUSE setup

Found while validating [PRP 01](/PRPs/01-correctness-fixes.md), and confirmed
**pre-existing** (reproduces identically against a clean checkout of HEAD, with
no PRP 01 changes applied): on this development machine (darwin/arm64),
`internal/mount/integration_test.go`'s `TestVirtualDir` fails —
`.janusfs was not found in root directory listing` — while every other
`fuseintegration`-tagged test in the same run, including a fresh mount-and-read
test added by PRP 01, passes. The failure is deterministic, not a timing flake
(reproduces identically across repeated runs).

Not yet root-caused. Candidates: a macFUSE version/config quirk on this specific
machine, or an ordering-dependent readdir buffering issue specific to this test's
sequence of operations. Needs investigation on a second macFUSE installation
before deciding whether this is environment-specific or a real, currently
unnoticed regression somewhere in the readdir path.

A related, definitely-real, now-fixed bug found in the same file:
`TestListxattrGating` called `syscall.Listxattr`, which is not defined in Go's
`syscall` package on darwin (only on linux) — a build break on any darwin
machine attempting `make integration` or `make leak-oracle`. Fixed by PRP 01 as
a drive-by, switching to `golang.org/x/sys/unix.Listxattr`, which is defined
identically on both platforms.
