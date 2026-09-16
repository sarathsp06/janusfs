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
    resource: /internal/execrunner/runner_linux.go
    title: exec source discovery
  - id: daemon
    resource: /cmd/janusfs/daemon.go
    title: daemon lifecycle
---

Every item below was found by reading source, not by running an exploit. The
item marked **unverified** needs a test to confirm before it is treated as
fact; the rest are readable directly from the code.

Eleven items closed so far — the agent hardlink bypass, the case-folding
bypass, `exec`'s silent cwd default, the duplicated control-protocol types,
`doctor`'s unrecoverable mountpoint ([PRP 01](/PRPs/01-correctness-fixes.md)),
the ungracefully-killed-daemon hang ([PRP 02](/PRPs/02-crash-recovery-watchdog.md)),
the unmemoized decision engine ([PRP 03](/PRPs/03-decision-cache.md)), the
read-path TOCTOU window ([PRP 05](/PRPs/05-dirfd-backing-layer.md)), the
open-handle revocation gap
([PRP 08](/PRPs/08-reload-revocation.md)), the hardcoded dev-only mock paths,
the masked-xattr redaction side channel, and the `--sandbox`-confined child's
reachability of the raw-bytes `/api/v1/reveal` endpoint (mooted structurally:
both the `--sandbox` flag and the endpoint were deleted —
[PRP 14](/PRPs/14-delete-sandbox-flag.md),
[PRP 15](/PRPs/15-delete-reveal-endpoint.md)) — have been removed from this
register. See those PRPs and [`log.md`](log.md) for what changed.

# 1. Unverified: whether the `readdir` inode-zeroing has a cost

`Getattr` zeroes `out.Ino` on every call (`internal/mount/janus_node.go:209`) so
go-fuse assigns synthetic inode numbers, avoiding "overriding ino" warnings when
a file is replaced by `git checkout` or an editor's rename-on-save. The comment
explains the motivation clearly.

**Unverified**: whether synthetic numbering breaks anything that relies on
stable inode identity across a remount — `find -samefile`, hardlink detection in
`tar`/`rsync`, or `du` deduplication. Worth one test before treating it as free.

# 2. macOS / non-Linux is unsupported by design (not a gap)

JanusFS enforces only on Linux (FUSE plus mount/user/network namespaces). The
binary still compiles and unit-tests on a macOS dev machine, but `janusfs mount`
and `janusfs exec` refuse at runtime off Linux. This is deliberate (SPEC §20),
not a defect to close: the former advisory macOS path was removed precisely
because it could never be a real boundary.
