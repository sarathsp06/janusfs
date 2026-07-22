# JanusFS

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://go.dev/dl/)
[![CI](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml/badge.svg)](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml)
[![Platform: macOS & Linux](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey.svg)](https://github.com/sarathsp06/janusfs)
[![Tests](https://img.shields.io/github/actions/workflow/status/sarathsp06/janusfs/ci.yml?label=tests&logo=github&branch=main)](https://github.com/sarathsp06/janusfs/actions)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**JanusFS gives AI agents a safe view of your files.** It mounts a sanitized mirror of a project directory, then enforces your rules at the filesystem boundary: normal files pass through, sensitive spans are replaced with `*`, and forbidden files return `EACCES`.

![JanusFS — Safe mirror illustration](docs/janus_art.png)

> In Roman myth, Janus is the two-faced god of doorways and transitions — he looks both ways. JanusFS stands at the doorway between your code and any untrusted agent, deciding which face of each file to show.

## In one minute

- Point JanusFS at a real source directory, then point your agent at the JanusFS mountpoint instead of the source.
- Configure policy with familiar `.gitignore`-style files: `.janusignore` hides paths, `.janusmask` redacts secrets inside otherwise useful files.
- Real files are never modified. Allowed reads pass through, Masked reads are byte-length-preserving redacted reads, and Hidden reads fail closed.
- A local daemon owns the mounts, keeps them alive, reloads rules on demand, and serves a dashboard showing what is protected and how the mount is being used.

The name comes from **Janus**, the Roman god of doors and gateways: JanusFS stands at the doorway between your real files and the agent, deciding which face of each file is safe to show.

---

## Contents

- [Why](#why)
- [How it works](#how-it-works)
- [Quickstart](#quickstart)
- [The daemon](#the-daemon)
- [The three faces](#the-three-faces)
- [Configuration files](#configuration-files)
  - [`.janusignore`](#janusignore)
  - [`.janusmask`](#janusmask)
  - [Global rules](#global-rules-machine-wide-defaults)
- [Built-in patterns](#built-in-patterns)
- [CLI reference](#cli-reference)
- [Security model](#security-model)
- [Comparison to alternatives](#comparison-to-alternatives)
- [Development Guide](docs/DEVELOPMENT.md)
- [Status](#status)
- [License](#license)

## Why

(Also: a short Janus backstory)

Janus watches doors and thresholds — places where context changes. The repository root is a kind of threshold: it contains both code the agent should reason about and secrets the agent must never see. JanusFS treats that boundary strictly. It decides, per-path and per-read, whether to hand the agent the real bytes, a redacted version, or nothing at all. This mirrors the myth: Janus decides what can pass and what cannot.

## How it works (flow diagram)

Here is a layered view of exactly what JanusFS does when an agent reads a file:

```mermaid
flowchart LR
  Agent["Agent (untrusted)"]
  Janus["JanusFS (FUSE mount)<br/>(policy snapshot)"]
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

If your renderer does not support Mermaid diagrams, here is a plain-text fallback:

```text
Agent -> JanusFS -> decision:
- ALLOWED -> passthrough to disk -> agent sees raw bytes
- MASKED  -> redaction layer (RAM cache) -> agent sees redacted bytes (same length)
- HIDDEN  -> deny (EACCES) -> agent cannot read
```


## How it works

AI coding agents need broad filesystem read access to be useful — and that access routinely includes `.env` files, private keys, credentials, cloud configs, and other things that should never end up in a prompt or a model's context.

The obvious workarounds all fail in some way:

- **Blanket-denying whole directories** breaks agents that legitimately need to see, for example, that `.env` exists to reference it in code.
- **Blanket-allowing everything** leaks secrets on the first `cat`, `grep`, or `find`.
- **Scrubbing before hand-off** is a one-shot: any file the agent touches later — or a fresh read after the scrub — still has the raw bytes.

JanusFS resolves this per-file, per-read, at the FS boundary. Rules use `.gitignore`-style globs plus a named pattern library — syntax your users already know — and every enforcement decision goes through one code path that fails **closed** on any error.

## How it works

A long-running **daemon** owns every mount. You drive it with short-lived CLI
commands (`janusfs mount`/`umount`) that talk to it over a local unix socket
and return immediately; the daemon holds the FUSE mounts, resumes them after a
reboot, and serves one dashboard for all of them. The agent only ever touches
the mountpoint — where each file wears one of three faces.

```
  ┌─ you (trusted) ─────────────┐          ┌─ agent (untrusted) ─────────┐
  │  janusfs mount <src>        │          │  cat / grep / open / readdir│
  │  janusfs umount <src>       │          └──────────────┬──────────────┘
  └──────────────┬──────────────┘                         │
                 │ command over unix socket               │ filesystem calls
                 │ ~/.janusfs/daemon.sock                 ▼
                 ▼                              ┌─────────────────────────┐
      ┌──────────────────────────┐  owns &     │  mountpoint (macFUSE)   │
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

The rule engine reads `.janusignore` and `.janusmask` from the mount root down (and from `~/.janusfs/config/` if it exists — see [Global rules](#global-rules-machine-wide-defaults)) and compiles them into an immutable snapshot. Every open, read, and readdir consults that snapshot. Redaction is **byte-length preserving** (`*` replaces every masked byte), so file sizes and offsets stay identical — tools don't see short reads and don't get confused. Errors — parser failures, missing rules, watcher lag, anything — resolve to **Hidden**.

## Quickstart

### 1) Install FUSE and JanusFS

#### Install FUSE runtime (Required)
- **macOS:** Install macFUSE: `brew install --cask macfuse`
- **Linux (Ubuntu/Debian):** `sudo apt-get install -y fuse3 libfuse3-dev`
- **Linux (RedHat/CentOS):** `sudo dnf install -y fuse3 fuse3-devel`

*Note for Apple Silicon users:* The macFUSE system extension must be approved once in *System Settings → Privacy & Security*, followed by a reboot. (See `SPEC.md` for why JanusFS uses macFUSE rather than FUSE-T).

#### Install JanusFS binary
- **Via Precompiled Release Binaries:** Download the latest tarball for your OS and architecture from the [GitHub Releases](https://github.com/sarathsp06/janusfs/releases) page, extract the `janusfs` binary, and move it to a directory in your `$PATH` (e.g., `/usr/local/bin`).
- **Via Go Toolchain:**
  ```bash
  go install github.com/sarathsp06/janusfs/cmd/janusfs@latest
  ```

### 2) Quickstart Setup & Usage

```bash
# 2) one-time setup: pick a mount root (where sanitized mirrors appear)
janusfs install                 # saves ~/.janusfs/settings.json; --global-rules also seeds machine-wide defaults

# 3) drop secure defaults into your project (or use --global-rules above)
cd my-project
janusfs init                    # writes .janusignore + .janusmask templates

# 4) preview what those rules will do BEFORE you mount
janusfs check                   # linter: zero-match globs, dir-mask, hidden-dir/global-floor negations
janusfs explain .env            # per-file trace: which rule decided this file's fate

# 5) start the daemon (owns mounts, serves the dashboard, resumes past mounts)
janusfs daemon &                # or run in its own terminal; opens the dashboard

# 6) mount — returns immediately; the daemon keeps it alive
janusfs mount .
# prints the mountpoint + dashboard URL. Point your agent at that mountpoint,
# never at the real path. Unmount later with:  janusfs umount .
```

The mountpoint always mirrors the source's full path under your mount root
(e.g. `~/.janusfs/mounts/Users/you/my-project`), so two sources never collide
and the location is fully predictable — there's no path override. To give a
mount a friendly name in the dashboard, pass `--name "My Project"`; it's a
display label only and never changes the path.

Every file that reaches the agent has been filtered (`$MP` is the mountpoint `janusfs mount` printed):

```bash
$ cat "$MP/.env"
API_KEY=****************************
DEBUG=true

$ cat "$MP/id_rsa"
cat: id_rsa: Permission denied

$ ls -la "$MP"                       # hidden files still LIST (with real sizes)
-rw-r--r--  ...   44 .env            #   so tools don't get confused
-rw-r--r--  ...  135 id_rsa          #   but reading fails closed
-rw-r--r--  ...  137 README.md       #   Allowed: passthrough
```

## The daemon

`janusfs daemon` is the one long-running process. Everything else is a thin
client that talks to it over `~/.janusfs/daemon.sock` and exits.

- **Owns every mount.** Each mounted source is a FUSE server running inside the
  daemon, with its own dashboard on an auto-assigned port. `janusfs mount <src>`
  hands the mount to the daemon and returns immediately — your terminal is free.
- **Reboot-safe.** Every mount is recorded in `~/.janusfs/mounts.json`; on start
  the daemon remounts all of them (except ones you explicitly `janusfs umount`).
- **One dashboard.** The daemon serves a combined index at
  `http://127.0.0.1:7381/` listing every live mount and linking to each mount's
  own dashboard. Change the port with `--ui-port`.
- **Clean shutdown.** Ctrl-C (or `SIGTERM`) unmounts everything and drains the
  dashboard.

```bash
janusfs daemon             # start it (add & to background, or use a terminal)
janusfs mount ~/proj       # hand a mount to the daemon; returns at once
janusfs mount ~/proj ~/pv  # or mount at a short path you choose (must be empty)
janusfs update ~/proj      # re-apply edited .janusignore/.janusmask (no remount)
janusfs path ~/proj        # print the mountpoint:  cd "$(janusfs path ~/proj)"
janusfs umount ~/proj      # unmount by source path OR mountpoint
janusfs paths              # show where settings, the registry, and rules live
```

Rules are applied on demand — there's no file watcher (watching a large tree
exhausts macOS file descriptors, and the native FSEvents API needs cgo, which
this project forbids). Editing a config file in the dashboard reloads
automatically; editing on disk, run `janusfs update` (or click **Reload rules**
in the dashboard). Reads are always correct regardless: every masked read
revalidates the file before serving.

If no daemon is running, `janusfs mount` says so; `janusfs umount` falls back to
a direct OS-level unmount so a stray mount can still be cleaned up.

## Recovering a stale or broken mount

If you see errors like "device not configured" or `ENXIO` when opening the
mountpoint, the kernel may still have a stale macFUSE mount while the daemon
has no live runtime for it. The CLI supports safe recovery steps:

- Try the daemon-aware unmount first:

  janusfs umount <mountpoint-or-src>

  This asks the running daemon to unmount (and prunes stale registry
  entries). If the daemon isn't running it falls back to an OS-level unmount.

- If a direct kernel mount remains, use the system unmount tools as a fallback:

  diskutil unmount <mountpoint>
  # or, if that fails:
  diskutil unmount force <mountpoint>

- To inspect mounts and pidfiles, use:

  janusfs paths
  janusfs doctor

  The `doctor` output includes any stale pidfiles and other runtime health
  indicators.

- If you prefer a manual cleanup, check for a backing registry entry:

  cat ~/.janusfs/mounts.json

  and remove a stale entry with `janusfs umount <mountpoint>` (daemon will
  prune it) or by editing the registry with care.


## First-run checklist

Before mounting for the first time, run this quick checklist to reduce friction:

1. Install and approve macFUSE (System Settings → Privacy & Security), then
   reboot if required.
2. Configure a mount root (recommended default is `~/.janusfs/mounts`) with:

   janusfs install

3. Seed secure defaults in your repo (or in `~/.janusfs/config`):

   cd my-project
   janusfs init

4. Lint rules and preview effects before mounting:

   janusfs check
   janusfs explain .env

5. Start the daemon and bring the mount up:

   janusfs daemon &
   janusfs mount .


## The three faces

| State       | Appears in listing | `getattr` size | `read` | `write` |
|-------------|--------------------|----------------|--------|---------|
| **Allowed** | yes                | real           | native passthrough | native passthrough |
| **Masked**  | yes                | **real** (byte-length preserved) | `*`-redacted content | **EACCES** (read-only) |
| **Hidden**  | yes                | real           | **EACCES** | **EACCES** |

**Precedence is strict:** `Hidden > Masked > Allowed`.
**Fail-closed:** any rule-resolution or parser error resolves that path to Hidden.

## Configuration files

### `.janusignore`

Pure `.gitignore` syntax. Paths matched here resolve to **Hidden**.

```gitignore
# hide entire files / trees
*.pem
*.key
id_rsa*
.aws/credentials
.terraform/

# directory-only patterns
node_modules/
build/

# negation: un-hide a specific file
!.aws/known_public_config
```

Rules are **hierarchical** and follow git semantics exactly: files further down the tree override shallower ones, and negation (`!`) can re-include a previously-excluded file — but **never** something under a directory that itself resolves to Hidden (a hidden ancestor short-circuits every descendant), and **never** a path the [global rule level](#global-rules-machine-wide-defaults) already hid (the global level is a fail-closed floor no in-tree rule can lift).

### `.janusmask`

Two dimensions per line: *which files* to redact, and *what inside them* to redact.

```
# <file-glob> [: <pattern>[, <pattern>...]]
# pattern = <built-in-name> | /<regex>/
# no pattern → whole-file mask

*.env*                              : env-value
**/*                                : aws-key, github-token, private-key, jwt
config/**/*.yaml                    : generic-secret, db-uri
secrets/*                                # no pattern → whole-file mask

# custom regex (RE2, anti-ReDoS)
**/*.log                            : /token=([A-Za-z0-9_-]{20,})/
```

- Masked spans are replaced byte-for-byte with `*`. **File sizes never change.**
- Capture group 1 of a regex is the masked span; without a group, the whole match is masked.
- Multiple lines for the same glob **accumulate** (set union of patterns).
- A literal `:` in a glob is escaped as `\:`.

### Global rules (machine-wide defaults)

Set rules that apply to every mount on your machine — for personal always-hide patterns you don't want to duplicate into every repo:

```bash
janusfs init --global    # writes ~/.janusfs/config/{.janusignore,.janusmask}
```

Global rules are treated as an **ancestor level above every mount root**, and act as a **fail-closed floor**: a repo's own rules (including negation) can freely override each other as usual, but no in-tree rule may re-include a path the global level Hid, or un-mask a path it Masked. `janusfs check`/`explain` flag any in-tree negation that has no effect for this reason.

The `.janusfs` directory layout mirrors other JanusFS on-disk state:

```
~/.janusfs/
├── config/           # global .janusignore, .janusmask
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

## CLI reference

| Command | Purpose |
|---------|---------|
| `janusfs install` | One-time setup: choose a mount root (saved to `~/.janusfs/settings.json`) so `janusfs mount <src>` needs no `--mount-root`. `--global-rules` also seeds `~/.janusfs/config/`. |
| `janusfs daemon` | Run the long-lived daemon: owns every mount, resumes recorded ones, serves the combined dashboard, accepts client commands. `--ui-port` (default 7381), `--no-open`, `--debug`. Ctrl-C unmounts everything. |
| `janusfs mount <src> [mountpoint]` | Ask the daemon to mount a sanitized view and return immediately. With no `[mountpoint]` the path mirrors `<src>` under the mount root; pass an empty `[mountpoint]` to mount at a short path you choose. `--name "<label>"` sets a friendly dashboard name only. |
| `janusfs update [src\|mountpoint\|configpath]` | Re-apply edited `.janusignore`/`.janusmask` rules without remounting. The argument may be the source, mountpoint, or a config/file path inside either tree (no arg = all mounts). |
| `janusfs path <src>` | Print the mountpoint for a mounted source, for `cd "$(janusfs path <src>)"`. |
| `janusfs umount <mountpoint\|src>` | Unmount via the daemon, by mountpoint or source path. Also prunes a stale registry entry / lingering mount; falls back to a direct OS unmount if no daemon is running. |
| `janusfs paths` | List the config/data paths JanusFS uses (settings, mounts registry, global rules, mount root) and whether each exists. |
| `janusfs init [dir]` | Write secure-default `.janusignore` + `.janusmask` to `[dir]` (default cwd). `--global` writes to `~/.janusfs/config/` instead. |
| `janusfs check [path]` | Static linter: unknown builtins, bad regex, zero-match globs, directory-mask globs, hidden-dir/global-floor negations that have no effect, duplicate rules. `--json` for machine output; exit 1 on errors. |
| `janusfs explain <path>` | Trace: why does one path resolve the way it does? Prints every rule that contributed. `--json` supported; `--root` selects the mount root (default cwd). |
| `janusfs doctor` | Runtime health: macFUSE status, active mounts, engine state, history DB stats, cache memory. |

All commands support `--help` and exit codes suitable for scripting. Errors are printed as a one-line cause; no Go stack traces reach the user.

### `janusfs explain` example

```
$ janusfs explain --root ~/proj ~/proj/.env
.env -> MASKED
  patterns: [env-value]
  deciding rule: /Users/you/proj/.janusmask:3
  evaluation trace (in order applied):
    /Users/you/.janusfs/config/.janusmask:6  "*.env*"  -> masked
    /Users/you/proj/.janusmask:3             "*.env*"  -> masked
```

### `janusfs check` example

```
$ janusfs check
/Users/you/proj/.janusmask
  [warn]:5  mask glob "**/application*.{yml,yaml}" matches no files under .
  [warn]:8  mask glob "secrets" also matches a directory, which can never be Masked — the directory match is a harmless no-op
      suggestion: rewrite to "secrets/**" if you meant to mask only the files inside

/Users/you/proj/sub/.janusignore
  [error]:2 rules: 2: patterns: compiling custom regex "[": error parsing regexp: missing closing ]: `[`

1 error(s), 2 warning(s), 0 info across 42 files, 7 directories.
```

## Security model

- **Trust boundary:** the mountpoint and the local HTTP dashboard. The agent is untrusted; the user operating the CLI is trusted.
- **Agents cannot weaken policy.** `.janusignore` and `.janusmask` are read-only through the mount, regardless of any user rule. The dashboard's mutating endpoints (edit a revealed file, save config, reload rules) require the per-mount bearer token and are operator tools — they act as the trusted user, not through the agent's mount.
- **Fail-closed under all faults.** Parser errors, cache corruption, redactor panics → paths read as Hidden (`EACCES`), never raw.
- **No content on disk.** Redacted bytes live only in RAM; the history DB stores per-path counters and coverage snapshots, **never** file contents.
- **Read path validates every time.** Every masked-file read revalidates `(mtime, size, inode)` against the cache key before serving — the authoritative change detector (there is no file watcher).
- **`~/.janusfs/` perms:** directory `0700`, files `0600`.

See [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) for the full boundaries / assets / leak-channels table, updated at every phase exit.

## Comparison to alternatives

| Approach | Handles secrets *inside* useful files? | Survives agent iterations? | Zero config-per-repo? | Perf near-native? |
|---|:-:|:-:|:-:|:-:|
| `.gitignore` / `.aiexclude` | ❌ (whole-file only) | ✅ | ⚠️ per-repo | ✅ |
| One-shot secret-scrubbing before agent hand-off | ⚠️ (frozen snapshot) | ❌ | ✅ | ✅ |
| Manually curated read-only copy | ❌ (whole-file only) | ❌ (out of date instantly) | ⚠️ setup | ✅ |
| Custom LLM tool wrappers that filter file reads | ⚠️ (per-tool, easily bypassed) | ⚠️ (per-tool discipline) | ⚠️ | ⚠️ |
| **JanusFS** | ✅ (per-span, byte-length preserving) | ✅ (FS boundary, per-read) | ✅ machine-wide via `~/.janusfs/config/` | ✅ (steady-state cache) |

## Development Guide

For details on building, formatting, running unit and FUSE integration tests, and validating the release configuration locally, see the **[Development Guide](docs/DEVELOPMENT.md)**.

## Status

**Currently:** Phases 0–4 landed. The engine (`.janusignore`/`.janusmask` discovery, resolution, precedence, fail-closed folding, the global-floor amendment), the built-in pattern library, the static linter (`janusfs check`) and per-file tracer (`janusfs explain`) all work against a real directory tree. The mount implements FR-7's full Allowed/Masked/Hidden matrix end-to-end — `internal/redact` (streaming size-preserving redaction) and `internal/provider` (RAM cache with stale-serve/rebuild) are wired into the FUSE adapter (`internal/mount`). Hot-reload (`internal/watch`) detects config and data-file changes, debounces, and triggers engine recompilation plus cache invalidation. The observability stack (`internal/obs` + `internal/api`) serves a live dashboard with stat cards, top paths, latency percentiles, and a real-time SSE event feed — with per-mount bearer token auth. History rollups (`internal/history`) persist to SQLite with configurable retention and batched writes off the event bus. Diagnostics include `janusfs doctor` for runtime health and `janusfs check` for static linting. Mounts are owned by a long-running daemon (`janusfs daemon`) that resumes them across reboots and serves one combined dashboard; `janusfs mount`/`umount` are thin clients over a local control socket.

Roadmap:

- [x] Phase 0 — walking-skeleton macFUSE passthrough
- [x] Phase 1 — rule engine, three-state resolution, `janusfs init`/`check`/`explain`
- [x] Phase 2 — pattern-based masking wired into the mount, leak oracle green
- [x] Phase 3 — hot reload, watcher, race-tight leak oracle
- [x] Phase 4 — dashboard, history, HTTP API, per-mount token auth
- [x] Phase 5 — diagnostics maturity (`janusfs doctor`, conflicts.json)

MVP (Phases 0–4) is complete.

## License

JanusFS is licensed under the [MIT License](LICENSE).
