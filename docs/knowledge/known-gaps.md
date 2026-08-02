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

Nine items closed so far — the agent hardlink bypass, the case-folding
bypass, `exec`'s silent cwd default, the duplicated control-protocol types,
`doctor`'s unrecoverable mountpoint ([PRP 01](/PRPs/01-correctness-fixes.md)),
the ungracefully-killed-daemon hang ([PRP 02](/PRPs/02-crash-recovery-watchdog.md)),
the unmemoized decision engine ([PRP 03](/PRPs/03-decision-cache.md)), the
read-path TOCTOU window ([PRP 05](/PRPs/05-dirfd-backing-layer.md)), the
open-handle revocation gap
([PRP 08](/PRPs/08-reload-revocation.md)), and the hardcoded dev-only mock paths — have been removed from this
register. See those PRPs and [`log.md`](log.md) for what changed.

# 1. Confirmed: xattr is a redaction side channel for MASKED files

`Getxattr` and `Listxattr` pass through for `Masked` files
(`internal/mount/janus_node.go:612`, `:626`) because both gate on
`denyHidden`, which only blocks `Hidden` — `gate`'s doc at
`internal/mount/janus_node.go:242` calls this deliberate ("xattr reads"
grouped with read/traverse operations). The redaction pipeline only ever
processes the data fork; nothing in `internal/redact` or `internal/provider`
touches extended attributes.

**Confirmed by test**, not merely theoretical: `TestMaskedXattrSideChannel`
(`internal/mount/integration_test.go`) writes a secret directly into an xattr
on a MASKED file's backing path, then reads it back through
`Listxattr`/`Getxattr` on the mount — full, unredacted content comes back.
Written but **not run** in this environment (macFUSE is installed but not
approved for this sandbox, so `make integration`/`make leak-oracle` cannot
mount here at all — the same failure `TestListxattrGating` hits unmodified).
Needs a run on a machine where the mount actually comes up before this is
closed.

This is a real, if narrow, leak path: on macOS in particular, editors,
Spotlight, and quarantine metadata routinely write xattrs (e.g.
`com.apple.metadata:*`, `com.apple.quarantine`) whose *values* can themselves
carry attacker- or agent-supplied bytes. The fix, if this environment's result
is confirmed elsewhere, is straightforward and narrow: change `Getxattr` and
`Listxattr`'s gate class from `denyHidden` to `denyNonAllowed` for MASKED
files specifically (HIDDEN already denies under either class) — there is no
xattr redaction to fall back to, so failing closed is the only correct
behaviour once MASKED, not ALLOWED, is what a caller is looking at. `Setxattr`
and `Removexattr` already use `denyNonAllowed`; only the two read-side
handlers are inconsistent with them.

# 2. Unverified: whether the `readdir` inode-zeroing has a cost

`Getattr` zeroes `out.Ino` on every call (`internal/mount/janus_node.go:209`) so
go-fuse assigns synthetic inode numbers, avoiding "overriding ino" warnings when
a file is replaced by `git checkout` or an editor's rename-on-save. The comment
explains the motivation clearly.

**Unverified**: whether synthetic numbering breaks anything that relies on
stable inode identity across a remount — `find -samefile`, hardlink detection in
`tar`/`rsync`, or `du` deduplication. Worth one test before treating it as free.

# 3. `TestVirtualDir` fails on at least one real macFUSE setup

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
