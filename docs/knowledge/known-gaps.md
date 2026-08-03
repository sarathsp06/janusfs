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
item marked **unverified** needs a test to confirm before it is treated as
fact; the rest are readable directly from the code.

Ten items closed so far — the agent hardlink bypass, the case-folding
bypass, `exec`'s silent cwd default, the duplicated control-protocol types,
`doctor`'s unrecoverable mountpoint ([PRP 01](/PRPs/01-correctness-fixes.md)),
the ungracefully-killed-daemon hang ([PRP 02](/PRPs/02-crash-recovery-watchdog.md)),
the unmemoized decision engine ([PRP 03](/PRPs/03-decision-cache.md)), the
read-path TOCTOU window ([PRP 05](/PRPs/05-dirfd-backing-layer.md)), the
open-handle revocation gap
([PRP 08](/PRPs/08-reload-revocation.md)), the hardcoded dev-only mock paths,
and the masked-xattr redaction side channel — have been removed from this
register. See those PRPs and [`log.md`](log.md) for what changed.

# 1. Unverified: whether the `readdir` inode-zeroing has a cost

`Getattr` zeroes `out.Ino` on every call (`internal/mount/janus_node.go:209`) so
go-fuse assigns synthetic inode numbers, avoiding "overriding ino" warnings when
a file is replaced by `git checkout` or an editor's rename-on-save. The comment
explains the motivation clearly.

**Unverified**: whether synthetic numbering breaks anything that relies on
stable inode identity across a remount — `find -samefile`, hardlink detection in
`tar`/`rsync`, or `du` deduplication. Worth one test before treating it as free.

# 2. `TestVirtualDir` fails on at least one real macFUSE setup

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

# 3. `--sandbox` (PRP 09) leaves the daemon's loopback API reachable from a confined child

`janusfs exec --sandbox`'s Seatbelt profile (`internal/execrunner/sandbox_darwin.go`)
denies the real source subtree and `~/.janusfs`, but leaves loopback
networking under `(allow default)` — denying it would break agents that
legitimately need localhost (dev servers, package installs against a local
registry). Consequence: a confined child can still reach
`GET /api/v1/reveal` (`internal/api/server.go:107`), which serves raw source
bytes.

**Not currently exploitable**: the bearer token is in-memory only
(`cmd/janusfs/runtime.go:87-92`), the static dashboard UI injects nothing
(`server.go:117`), and the control socket's dashboard URL carries no token
(`daemon.go:510`) — so a confined child has no way to obtain the token to call
the endpoint. Recorded as a known gap rather than closed because it is a
structural reachability issue (the endpoint is one token away from a raw-bytes
read), not a defense the profile currently provides; a future hardened profile
should scope-deny the daemon's port specifically rather than all loopback.
