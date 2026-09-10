# PRP 12 — Delete the macOS enforcement track

**Size:** M · **Blocked by:** nothing · **Status:** done

## Why

Every major harness now ships vendor-signed, kernel-backed confinement on
macOS (Codex CLI and Gemini CLI use Seatbelt directly; Anthropic's srt wraps
Seatbelt/bubblewrap). A daemon-side identity heuristic that a `setsid` evades
will never beat that, and has no audience. The subsystem existed only to feed
PRP 07's path-preserving mode, which is now rejected.

## Change (all deletions)

- `internal/procid/` (10 files) — deleted. Its PRP 06 references die with it.
- `PRPs/06-process-identity.md`, `PRPs/07-macos-path-preserving.md` — deleted;
  their rationale moves to SPEC §20 rejected designs (vendor-signed kernel
  Seatbelt beats a daemon heuristic; no audience remains).
- `docs/knowledge/process-identity.md` — deleted; index and cross-references
  updated.
- macOS `exec` becomes one honest sentence: discover source, ensure mount,
  set cwd to mountpoint, scrub env, run. See PRP 13/14 for the argv rewriter
  and Seatbelt wrapper deletions that complete the story.

## Acceptance

No file in the tree implements or plans per-operation caller identity.
SPEC §20 records why, so it is not re-proposed.
