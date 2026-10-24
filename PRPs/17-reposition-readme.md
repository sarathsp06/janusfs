# PRP 17 — README repositioning: the masking layer that composes with your sandbox

**Size:** M · **Blocked by:** 10 (for enforcement claims), 12–15 (story must match tree) · **Status:** done (initial rewrite; harness recipes pending PRP 11)

## Why

2026 harnesses ship native sandboxing (Seatbelt, Landlock, bubblewrap,
srt, devcontainers). All of it is binary allow/deny. Deny breaks agents;
allow leaks secrets — `cat .env | curl` the moment network opens. JanusFS's
unique value is the third answer: **masked** — byte-length-preserving
redaction at the FS boundary, covering every channel (read tool, Bash, git,
subprocesses) at once. The README must sell that and stop competing on deny.

## Change

- Framing up top: "Sandboxes answer allow/deny. JanusFS adds the third
  answer: masked. Compose it with the sandbox you already run."
- Comparison table gains rows for srt/Seatbelt, Landlock, bubblewrap, Docker:
  all FAIL on "handles secrets inside useful files".
- New section: the prompt-injection exfil case — deny-sandbox with network
  open doesn't stop `cat .env | curl`; masking does, because raw bytes never
  enter the process tree.
- Composition recipes: `janusfs exec` inside a devcontainer / inside srt /
  alongside Codex's sandbox. Smoke-test each once PRP 11's Linux box exists.
- macOS section shrinks to one paragraph: advisory masking mount;
  enforcement = Linux (or the existing Docker recipe); your harness's own
  sandbox is the deny layer.
- No document anywhere still promises macOS path-preserving mode as roadmap.

## Acceptance

README makes no enforcement claim PRP 10 didn't verify; "sandbox" appears
only as a thing JanusFS composes with, never as what it is.
