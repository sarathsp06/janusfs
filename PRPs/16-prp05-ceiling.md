# PRP 16 — Resolve PRP 05's dangling half

**Size:** S (decision) · **Blocked by:** 10 · **Status:** open

## Why

PRP 05's status reads "read path done; mutations still via LoopbackNode." Its
original driver was macOS overmount (PRP 07, now deleted). The read-path dirfd
work is banked (TOCTOU closed where it matters). A permanently dangling
half-status invites re-derivation every time someone reads the table.

## Change

Decide, don't drift. After PRP 10's overmount spike reports:

- If direct overmount (or the shadow bind) holds on real Linux and mutations
  through LoopbackNode are path-resolved safely under it: mark the remainder
  as a recorded ceiling — `ponytail:` comment in
  `internal/mount/janus_node.go` naming the ceiling (mutations resolve by
  path, safe only under a private namespace) and the upgrade path (dirfd
  mutations), and set PRP 05's row to "done with recorded ceiling".
- If not: finish the mutation half (dirfd `renameat2`/`unlinkat`/`openat`
  writes) as its own branch.

## Acceptance

PRP 05's row in the gating table reads "done" or "done-with-recorded-ceiling",
not a dangling half-status; the ceiling, if chosen, is named in code where the
next reader will trip over it.
