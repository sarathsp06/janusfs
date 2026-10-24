# Landscape: who else solves this, and why `janusfs exec` is worth using

Surveyed October 2026 from public docs, issue trackers, and forums (web only;
links are the evidence). This page answers two questions a sceptical user
asks first: *does anything else already do this?* and *does the tool behave
the way the tools I already use behave?*

## 1. Nobody masks in place at the filesystem layer

Every tool that keeps secrets away from an agent does one of three things:

| Approach | Examples | What the agent sees | Covers `cat`/`grep`/subprocesses? |
|---|---|---|---|
| Deny the file | Claude Code `permissions.deny Read(.env)`, `.cursorignore`, Codex read-only `.git/`/`.codex/` | nothing, or an error | **No** for `.cursorignore` (terminal and MCP tools are exempt: [forum](https://forum.cursor.com/t/cursor-reads-env-even-though-it-is-on-cursorignore/136998)); deny rules have had enforcement gaps ([#46672](https://github.com/anthropics/claude-code/issues/46672)) |
| Swap in a placeholder, re-inject at the network edge | Claude Code `sandbox.credentials` `"mode":"mask"` (Linux only, whole-file unless an `extract` regex; macOS falls back to deny; mask entries ignored in repo settings — [docs](https://code.claude.com/docs/en/sandboxing)), Docker Sandboxes, nono, OpenShell | a sentinel string of a different length | Yes, but the file's shape is gone, and it needs a TLS-terminating proxy |
| Redact inside one agent tool | agentmask `safe_read` (`KEY=[REDACTED:…]`), agent-vault, claude-redact-env hook, agent-shell-secret-mask | keys kept, values replaced by labels | **No**: only the tool that was wrapped |

Two searches for a FUSE- or kernel-level view that keeps a file's exact
shape and stars out only the secret spans, for *every* process, found
nothing. That is the ground JanusFS stands on: `cat .env` prints
`API_KEY=****************` with the real length, `stat` reports the real
size, lockfile and offset-based tooling keep working, and the boundary is a
mount namespace the agent's subprocesses cannot opt out of. The scouts'
summary line, verbatim: "No tool keeps lengths, so this is unclaimed ground."

## 2. The complaints people actually file, and what this PR does about each

| Complaint (evidence) | What `janusfs exec` now does |
|---|---|
| "How do I tell the sandbox is on?" — request for a status-line indicator ([claude-code #18522](https://github.com/anthropics/claude-code/issues/18522)); Gemini CLI ships a footer indicator; `nix shell` sets no inside-variable and users complain ([nix #6677](https://github.com/NixOS/nix/issues/6677)) | Two-line **start banner** on stderr naming the tree and the network mode; **`JANUSFS_EXEC=1`** in the child so a prompt can show it |
| Silent fallbacks: `sandbox.enabled` ignored when socat is missing ([#34044](https://github.com/anthropics/claude-code/issues/34044)); Cursor "Sandbox is unavailable. Falling back…" ([forum](https://forum.cursor.com/t/sandbox-is-unavailable-falling-back-to-allowlist-mode-for-this-session/152095)); landrun `--best-effort` drops rules | JanusFS already **refuses** rather than degrades; now **`--dry-run`** and **`doctor`** say *before* launch whether this host can enforce, with the remedy |
| Agents disabling their own sandbox with no indication ([#72960](https://github.com/anthropics/claude-code/issues/72960)); "nearly pointless… false sense of security" ([HN](https://news.ycombinator.com/item?id=47552146)) | The boundary is the kernel's, not a flag the agent can flip; the **end-of-session summary** reports what was masked and denied, so an empty session is also visible |
| Over-strict sandboxes break builds: Java DNS bypassing the proxy ([#11481](https://github.com/anthropics/claude-code/issues/11481)), Codex "showstopper" on dependency installs ([codex #53187](https://github.com/openai/codex/issues/53187)) | Network isolation stays **opt-in**; **`--dry-run`** previews exactly which files are hidden or masked before the agent hits them; `--net=host` remains one flag away, noted when it loosens a repo default |
| A repo must not be able to weaken protection: Claude ignores `mask` in repo settings; Cursor admin policy overrides the repo file; git honours `safe.directory` only outside the repo | Per-project **`exec.net` is tighten-only**; no trust prompt is needed because a cloned repo can only ask for *more* isolation |
| Hooks and tools echoing secrets into transcripts ([#58173](https://github.com/anthropics/claude-code/issues/58173), [#44868](https://github.com/anthropics/claude-code/issues/44868)) | The agent never holds the bytes, so there is nothing to echo. The new feedback lines carry paths and counts only (see `docs/THREAT_MODEL.md`) |

## 3. Conventions borrowed from tools people already trust

| Convention | Precedent | Here |
|---|---|---|
| Inside marker as an env var, prompt left alone | `VIRTUAL_ENV`, `IN_NIX_SHELL`, `TMUX`, distrobox `CONTAINER_ID` | `JANUSFS_EXEC=1`; bash one-liner in `janusfs shell --help` |
| No command → a shell | firejail, unshare, chroot, `distrobox enter`, `systemd-run --shell` | `janusfs shell` (explicit, so `exec` with no command still refuses in scripts) |
| Silent on success, diagnostics on stderr only | bwrap, docker, landrun; firejail's "Reading profile…" and stdout pollution are the filed anti-patterns ([#3354](https://github.com/netblue30/firejail/issues/3354), [#1677](https://github.com/netblue30/firejail/issues/1677)) | Two lines on stderr; `--quiet` is byte-silent but never hides security warnings |
| `--dry-run` prints the real plan, machine-readable too | `distrobox enter --dry-run`, bwrap `--json-status-fd` | `exec --dry-run [--json]` |
| Exit codes 125/126/127 | chroot, docker/podman, GNU timeout | wrapper 125, not executable 126, not found 127 |
| Nesting named, not refused | tmux and `$TMUX` | banner line "nested: …"; verified useful (inner `--net=none`) |
| Doctor: ✓/✗ per check with a remedy | `flutter doctor` | `doctor`'s second line is exec readiness |

## 4. What the survey argues against, and the answer

**"Support macOS for real"** — most sandbox complaints come from Mac users,
and Claude's own masking falls back to deny there. JanusFS deliberately does
not: there is no per-process mount namespace on macOS, so any macOS mode
would be the "false sense of security" users already complain about
(SPEC §20, rejected designs). The answer this PR gives a Mac user is
`janusfs exec --dry-run`, which works on a Mac and shows exactly what the
Linux run will do, and `doctor`, which says plainly that enforcement needs
a Linux container or host.

**"Give the real value a path to the process that needs it"** — sentinel
injection at a proxy (Claude Code, Docker Sandboxes, nono) is proven.
JanusFS does not do it: its contract is that the agent process tree never
holds the secret. A build that genuinely needs a live credential belongs
outside the view, or behind a proxy the user already runs. Recorded here so
it is not re-proposed as a gap.

Sources are the scouts' reports of 2026-10-24; see `PRPs/20-exec-usability.md`
for the design record this informed.
