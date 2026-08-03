# macOS Seatbelt enforcement — feasibility spike

**Status:** implemented, flag-gated, in [PRP 09](/PRPs/09-macos-seatbelt-exec.md) — `janusfs exec --sandbox` (`internal/execrunner/sandbox_darwin.go`). This document is the spike that motivated it; see the PRP for the as-built design, and [platform-isolation.md](knowledge/platform-isolation.md)'s "Option D" for the shipped behavior and the two correctness properties (canonical firmlink paths, mountpoint re-allowed last) that were not obvious until tested end-to-end.
**Question:** can macOS `sandbox-exec` (Seatbelt) give an agent's whole process tree a real, kernel-enforced boundary — denying access to the real source path while allowing the JanusFS mountpoint — *without* a kernel extension?

**Answer: yes, for a plain-CLI process tree** (validated on Darwin 25.5 / macOS 26 with `/bin/cat`, `/bin/bash`, `/usr/bin/find`). Whether it survives wrapping a *real* signed/Electron harness (Cursor.app, the Claude Code app) is untested — see Open risks. Cursor is publicly documented as using Seatbelt for its own macOS agent sandbox, which is corroborating prior art, not proof that it transfers to our shape.

## Why this matters

`janusfs exec` on Linux confines the agent in a private mount namespace — the unfiltered tree literally does not exist inside it, for both read and write. macOS has no per-process mount namespace available to a third-party tool, so `janusfs exec` on macOS is only advisory: the real source stays reachable at its own path. Seatbelt is the one macOS mechanism that can confine an **entire subprocess tree** at the kernel level and is usable by an unprivileged, non-entitled tool. It would make "macOS enforced" true — for **Hidden** (deny). **Masked** is still served by the FUSE decision: Seatbelt can allow/deny but cannot rewrite bytes, so it constrains the agent to the *masked view* rather than performing masking itself.

## Validated profile

Deny read **and write** of the real source subtree; allow everything else (including the mountpoint):

```scheme
(version 1)
(allow default)
(deny file-read*  (subpath "/private/var/.../real-source"))
(deny file-write* (subpath "/private/var/.../real-source"))
```

`file-write*` matters: a read-only deny leaves the confined agent able to truncate or overwrite the real source at its own path. Through FUSE, both Masked and Hidden already reject write-intent opens (`internal/mount/janus_node.go:355`); at the *real* path there is no such gate, so Seatbelt must add it. Legitimate writes go via the mountpoint, so denying writes to the real path is effectively free.

Reproduction (all commands run and observed):

```bash
d=$(mktemp -d); mkdir "$d/real" "$d/mount"
echo "API_KEY=supersecret" > "$d/real/.env"
echo "hello=world"        > "$d/mount/ok.txt"
REAL=$(cd "$d/real" && pwd -P)          # -P is REQUIRED (see gotcha 1)
MNT=$(cd "$d/mount" && pwd -P)
printf '(version 1)\n(allow default)\n(deny file-read* (subpath "%s"))\n(deny file-write* (subpath "%s"))\n' "$REAL" "$REAL" > "$d/deny.sb"

sandbox-exec -f "$d/deny.sb" cat "$REAL/.env"                                 # deny  -> Operation not permitted
sandbox-exec -f "$d/deny.sb" cat "$MNT/ok.txt"                                # allow -> hello=world
sandbox-exec -f "$d/deny.sb" bash -c "cat '$REAL/.env'"                       # deny  (child)
sandbox-exec -f "$d/deny.sb" bash -c "find '$REAL' -type f -exec cat {} \;"   # deny  (grandchild)
sandbox-exec -f "$d/deny.sb" bash -c "echo x >> '$REAL/.env'"                 # deny  (write to real)
sandbox-exec -f "$d/deny.sb" bash -c "echo x >> '$MNT/ok.txt'"                # allow (write to mount)
sandbox-exec -f "$d/deny.sb" bash -c "sandbox-exec -p '(allow default)' cat '$REAL/.env'"  # deny (nested loosen refused)
```

### Results

| Case | Expected | Result |
|---|---|---|
| Direct read of real secret | deny | ✅ `Operation not permitted` |
| Read of mountpoint file | allow | ✅ served |
| Child (`bash -c cat`) reads real secret | deny | ✅ denied |
| Grandchild (`find … -exec cat`) reads real secret | deny | ✅ denied — **whole tree confined** |
| Write to real source path | deny | ✅ denied, file unchanged |
| Write into the mountpoint | allow | ✅ succeeded (`WROTE-OK`) |
| Confined child re-execs `sandbox-exec` with a permissive profile | deny loosen | ✅ `sandbox_apply: Operation not permitted` — **cannot un-sandbox from within** |

Two of these carry the design: the **grandchild** result shows the confinement is inherited by the entire subprocess tree (not just the direct child), and the **nested-loosen** result validates the load-bearing "cannot step around" assumption — a confined process can narrow its sandbox further but cannot widen it.

Note this is what confining the *process tree* buys over confining a *channel*: it covers processes JanusFS never spawned. A channel-owning design (an MCP `run_command` backed by `janusfs exec`, a JanusFS-owned pty) confines *its own* subprocess tree fine — namespace/sandbox inheritance is transitive. What it cannot confine is the channels it does **not** own: the harness's native `read_file`/`Grep`/`Glob` and its own Bash tool, which still read raw disk. Seatbelt wrapping the whole harness closes those too.

## Gotchas (both fail **open** — a wrong profile silently allows everything)

1. **Paths must be canonical.** Seatbelt matches the *resolved* path. `/var/folders/...` is a symlink to `/private/var/folders/...`; a `subpath` using the unresolved form matches nothing and every read is allowed — the first spike attempt failed exactly this way. Always resolve with `pwd -P` / `realpath`. **Firmlinks too, not just symlinks:** a repo under `/Users/x` is also reachable as `/System/Volumes/Data/Users/x` (APFS firmlink). Whether Seatbelt collapses firmlinks to one canonical form is **unverified**; until it is, a hardened profile should deny both forms.
2. **Rule order is last-match-wins.** `(allow default)` then a later `(deny …)` is correct; reversing them re-allows the path.

## Open risks — not yet tested

- **Signed / Electron harnesses (Cursor.app, Claude Code app, JetBrains).** TCC prompts, hardened-runtime, and existing entitlements may reject a wrapping sandbox. The spike used plain CLIs; wrapping a plain-CLI agent (`aider`, a shell) is the low-risk first target, an app bundle is the unknown.
- **Positive-path completeness.** Verified that a *write into the mount* succeeds, but not that a full real workload (`git`, `npm install`, a compile) still completes under the profile. "`cat` denied" and "usable dev environment" are separate claims; the second is unvalidated.
- **Deny-set completeness — the JanusFS API is the shortest route to the asset, ahead of `~/.aws`.** Under `(allow default)` the confined tree keeps loopback networking, read of `~/.janusfs`, and the daemon control socket. The dashboard's `GET /api/v1/reveal` serves **raw source bytes** (`internal/api/server.go:107`, `:320`). This is *not* a confirmed bypass — the bearer token is in-memory only (`cmd/janusfs/runtime.go:87-92`), the static UI handler injects nothing (`server.go:117`), and the control socket's dashboard URL carries no token (`daemon.go:510`) — but it is a raw-bytes endpoint one token away, and a hardened profile should deny loopback + `~/.janusfs` and treat this before external secrets like `~/.aws` or `.docker/config.json`. Secrets reachable *through* the source (a symlink to `~/.aws`, a script sourcing `$HOME/.secrets`) also need their own deny rules or a default-deny-read profile with an allowlist (stronger, but far more likely to break the toolchain — a scope decision).
- **Coprocess architecture.** The FUSE daemon must live **outside** the sandbox profile (it serves the mount the agent reads). The agent tree is sandboxed; the daemon is not. Composes cleanly in principle — verify the mountpoint stays readable while the real path is denied.
- **Deprecation.** `sandbox-exec` is long-deprecated but functional (no fatal error on Darwin 25.5). Apple could remove it and there is no supported public replacement for third-party tools — a real long-term risk.

## The `.git` write-back caveat (applies to every enforced-view design, not just Seatbelt)

`.git/` is Allowed → passthrough to the **real** object store (no special-casing exists in the enforcement path — `rg '\.git' internal` finds none). So inside any enforced view — Linux namespace *or* a Seatbelt-confined tree — `git add <masked-file> && git commit` stages the `****` bytes into real git. Git's stat cache (real mtime + byte-length-preserved size) hides it from plain `git status`, which is why it hasn't surfaced. This repo demonstrates the setup: `git ls-files testdata/demo-tree/` tracks `.env` (Masked) and `id_rsa` (Hidden) — so `git add` of the masked `.env` through a view of that tree stages asterisks. (Real-world Spring repos add `application*.yml`, which the default `init` template also masks, widening the blast radius.) Mitigations to decide before shipping any exec-based enforcement: route the agent's git to a scratch clone/worktree, or a `janusfs git` wrapper that skips masked paths on `add`/`commit`. Hiding `.git` closes it but breaks git for the agent.

## Recommendation (implemented — see status line above)

Built as `janusfs exec --sandbox` behind a flag, for plain-CLI harnesses, using a **canonical-path (symlink + firmlink), last-match-wins, default-allow + deny-source-read + deny-source-write + mountpoint-re-allow-last** profile, plus a read-only deny of `~/.janusfs` as cheap defense-in-depth (JanusFS's own config/state, not the reveal-API side channel specifically — see the residual-risk note above, which v1 left open rather than denying loopback wholesale, since that would break agents needing localhost). Shipped as kernel-enforced **Hidden** with Masked still served by FUSE; the two-tier framing stays honest: the Linux namespace is the reference boundary (read + write); Seatbelt is the macOS equivalent for deny, gated on the open risks above — chiefly whether it survives a real harness and a full build, both still untested.
