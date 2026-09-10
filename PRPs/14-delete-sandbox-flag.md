# PRP 14 — Delete `--sandbox` (macOS Seatbelt wrapper)

**Size:** M · **Blocked by:** nothing · **Status:** done

## Why

PRP 09's `sandbox-exec` wrapper duplicated what Codex CLI, Gemini CLI, and
Anthropic srt ship natively — vendor-signed and TCC-aware, which JanusFS's
wrapper explicitly was not (never validated against signed/Electron
app-bundle harnesses). It also created the confusing two-tier macOS story
("advisory, but --sandbox…") and was the source of the confined-child
known-gap (#3: one bearer token away from `/api/v1/reveal`).

Harness-native Seatbelt is the recommended deny layer on macOS; JanusFS's
value there is masking, which is FUSE-served and needs no sandbox.

## Change (deletions + simplification)

- Delete `sandbox_darwin.go`, its unit + integration tests, and
  `docs/SEATBELT_SPIKE.md`.
- `exec.go` (cobra): remove the `--sandbox` flag scan; anything before `--`
  is now an error. Help text rewritten: Linux = kernel-enforced, macOS =
  advisory + "use a Linux container for enforcement".
- `execrunner.Run` loses the `sandbox bool` parameter on both platforms.
- Closes known-gap #3 as a side effect (the reveal endpoint also died — PRP
  15); SPEC §20 records the spike's findings so they aren't re-derived.

## Acceptance

`janusfs exec --sandbox -- x` fails with "unrecognized flag"; no
`sandbox-exec` reference remains in the tree; help text contains no
enforcement claim for macOS.
