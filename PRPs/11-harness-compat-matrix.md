# PRP 11 — Harness compatibility under the uid-0 illusion

**Size:** S · **Blocked by:** 10 · **Status:** open — needs a real Linux box

## Why

`CLONE_NEWUSER` makes the namespaced child believe it is uid 0 (accepted risk,
SPEC §20). Some tools change behavior as root (npm scripts, git safe.directory,
sandbox self-checks in agent harnesses). Before recommending
`janusfs exec -- <harness>`, the three harnesses people actually run must be
exercised for real.

## Change

Matrix test, manual or CI, on real Linux: `janusfs exec -- claude`,
`janusfs exec -- codex`, `janusfs exec -- aider`. For each: does it start,
authenticate, read a Masked file (sees masked bytes), and fail to see Hidden
files? Record per-harness results in a new
`docs/knowledge/harness-compat.md`, linked from README's exec section,
replacing today's implicit "test yours before relying on it".

Known interaction to probe explicitly: harnesses that spawn their own sandbox
(Codex Landlock, Claude Code srt/bubblewrap) inside the JanusFS namespace —
nested userns creation may be denied. Record the failure mode and the
workaround (run the harness sandbox OUTSIDE, `janusfs exec` inside — the
recommended composition anyway).

## Acceptance

A table of harness × works/breaks/workaround exists in the knowledge bundle
and README links to it. No harness is listed from inference; every row was run.

## If this is wrong

If no harness works under the uid-0 illusion, the accepted risk graduates to
a design problem: consider mapping to the real uid instead (loses the ability
to mount as non-root on some kernels) — decision record required.
