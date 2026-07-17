# JanusFS

**A sanitized filesystem view for AI agents.** JanusFS mounts your project directory and shows every file through one of three faces — plain, redacted, or hidden — so an agent gets exactly the access you intend, and never a byte more.

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://go.dev/dl/)
[![macOS via FUSE-T](https://img.shields.io/badge/platform-macOS%20%28FUSE--T%29-lightgrey.svg)](https://www.fuse-t.org/)
[![License TBD](https://img.shields.io/badge/license-TBD-lightgrey.svg)](#license)

> In Roman mythology, **Janus** is the god of doorways, transitions, and gates — depicted with two faces, one looking forward and one back. Nothing passes through a door watched by Janus without him deciding what's seen on each side.
>
> That's the whole idea here. Every file behind a JanusFS mount has two faces: the real one on disk, and the one an agent is allowed to see. JanusFS stands at the doorway and decides which face to show — plaintext, redacted, or not at all — before a single byte crosses through.

---

## Contents

- [Why](#why)
- [How it works](#how-it-works)
- [Quickstart](#quickstart)
- [The three faces](#the-three-faces)
- [Configuration files](#configuration-files)
  - [`.janusignore`](#janusignore)
  - [`.janusmask`](#janusmask)
  - [Global rules](#global-rules-machine-wide-defaults)
- [Built-in patterns](#built-in-patterns)
- [CLI reference](#cli-reference)
- [Security model](#security-model)
- [Comparison to alternatives](#comparison-to-alternatives)
- [Building and hacking](#building-and-hacking)
- [Status](#status)
- [License](#license)

## Why

AI coding agents need broad filesystem read access to be useful — and that access routinely includes `.env` files, private keys, credentials, cloud configs, and other things that should never end up in a prompt or a model's context.

The obvious workarounds all fail in some way:

- **Blanket-denying whole directories** breaks agents that legitimately need to see, for example, that `.env` exists to reference it in code.
- **Blanket-allowing everything** leaks secrets on the first `cat`, `grep`, or `find`.
- **Scrubbing before hand-off** is a one-shot: any file the agent touches later — or a fresh read after the scrub — still has the raw bytes.

JanusFS resolves this per-file, per-read, at the FS boundary. Rules use `.gitignore`-style globs plus a named pattern library — syntax your users already know — and every enforcement decision goes through one code path that fails **closed** on any error.

## How it works

```
       ┌────────────────────┐          ┌───────────────────────┐
       │  AI agent, editor, │          │  Real files on disk   │
       │  build tool, shell │          │  (your source tree)   │
       └─────────┬──────────┘          └──────────┬────────────┘
                 │  reads/writes                   ▲
                 ▼                                 │
      ┌─────────────────────┐                      │
      │  JanusFS mountpoint │  ── Allowed  ──────► │
      │  (FUSE-T on macOS)  │  ── Masked   ──────► redact and serve
      │                     │  ── Hidden   ──────► EACCES
      └─────────────────────┘
```

The rule engine reads `.janusignore` and `.janusmask` from the mount root down (and from `~/.janusfs/config/` if it exists — see [Global rules](#global-rules-machine-wide-defaults)) and compiles them into an immutable snapshot. Every open, read, and readdir consults that snapshot. Redaction is **byte-length preserving** (`*` replaces every masked byte), so file sizes and offsets stay identical — tools don't see short reads and don't get confused. Errors — parser failures, missing rules, watcher lag, anything — resolve to **Hidden**.

## Quickstart

```bash
# 1) install
brew install --cask fuse-t     # macOS FUSE implementation, no kernel extension
go install github.com/sarathsp06/janusfs/cmd/janusfs@latest

# 2) drop secure defaults into your project (or ~/.janusfs/config for machine-wide)
cd my-project
janusfs init                    # writes .janusignore + .janusmask templates

# 3) preview what those rules will do BEFORE you mount
janusfs check                   # linter: zero-match globs, dir-mask, hidden-dir negations
janusfs explain .env            # per-file trace: which rule decided this file's fate

# 4) mount
mkdir /tmp/my-project-view
janusfs mount . /tmp/my-project-view
# point your agent at /tmp/my-project-view — never at the real path
```

Every file that reaches the agent has been filtered:

```bash
$ cat /tmp/my-project-view/.env
API_KEY=****************************
DEBUG=true

$ cat /tmp/my-project-view/id_rsa
cat: id_rsa: Permission denied

$ ls -la /tmp/my-project-view/       # hidden files still LIST (with real sizes)
-rw-r--r--  ...   44 .env            #   so tools don't get confused
-rw-r--r--  ...  135 id_rsa          #   but reading fails closed
-rw-r--r--  ...  137 README.md       #   Allowed: passthrough
```

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

Rules are **hierarchical** and follow git semantics exactly: files further down the tree override shallower ones, and negation (`!`) can re-include a previously-excluded file — but **never** something under a directory that itself resolves to Hidden (a hidden ancestor short-circuits every descendant).

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

Global rules are treated as an **ancestor level above every mount root** — the lowest precedence of all, so any repo's own rules can override them. This is fail-closed-safe: a global Hidden rule can only be widened into visibility by an explicit local negation, and a global Masked rule is only ever narrowed or overridden by more specific in-tree rules.

The `.janusfs` directory layout mirrors other JanusFS on-disk state:

```
~/.janusfs/
├── config/           # global .janusignore, .janusmask
├── run/              # pidfiles for active mounts
└── history/          # SQLite rollups for the dashboard (Phase 4)
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
| `janusfs init [dir]` | Write secure-default `.janusignore` + `.janusmask` to `[dir]` (default cwd). `--global` writes to `~/.janusfs/config/` instead. |
| `janusfs mount <src> <mountpoint>` | Mount a sanitized view. Blocks; unmount with SIGINT/SIGTERM or `janusfs umount`. |
| `janusfs umount <mountpoint>` | Unmount a running JanusFS. Best-effort signal to the owning process (via pidfile). |
| `janusfs check [path]` | Static linter: unknown builtins, bad regex, zero-match globs, directory-mask globs, hidden-dir negations, duplicate rules. `--json` for machine output; exit 1 on errors. |
| `janusfs explain <path>` | Trace: why does one path resolve the way it does? Prints every rule that contributed. `--json` supported; `--root` selects the mount root (default cwd). |
| `janusfs doctor` | *(Phase 5)* Runtime health: FUSE-T status, active mounts, rule counts, cache memory, watcher health. |

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

- **Trust boundary:** the mountpoint and (once Phase 4 lands) the local HTTP dashboard. The agent is untrusted; the user operating the CLI is trusted.
- **Agents cannot weaken policy.** `.janusignore` and `.janusmask` are read-only through the mount, regardless of any user rule. The dashboard API has no mutating endpoints.
- **Fail-closed under all faults.** Parser errors, watcher overflow, cache corruption, redactor panics → paths read as Hidden (`EACCES`), never raw.
- **No content on disk.** Redacted bytes live only in RAM; the (Phase 4) history DB stores per-path counters and coverage snapshots, **never** file contents.
- **Read path validates every time.** The watcher is advisory — every masked-file read revalidates `(mtime, size, inode)` against the cache key before serving.
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

## Building and hacking

```bash
# build the binary (macOS arm64/amd64)
make build

# unit tests
make test
make test-race        # -race
make coverage         # writes coverage.out + coverage.html, prints summary

# format / lint
make fmt              # gofmt + goimports
make vet              # go vet
make lint             # golangci-lint (optional)

# integration (requires FUSE-T installed)
make integration      # -tags fuseintegration
make leak-oracle      # sentinel-secret scan through real mount

# releases (goreleaser; produces darwin-universal tarballs + checksums + SBOM)
make release-snapshot # local build, no publish
make release-check    # goreleaser config validation
```

`make help` (default target) prints every available target with descriptions.

Full engineering contract, phased build plan, and normative Go interfaces: **[`SPEC.md`](SPEC.md)**.
Product-level plan and non-goals: **[`PLAN.md`](PLAN.md)**.
Conventions for anyone (agents included) writing JanusFS code: **[`AGENTS.md`](AGENTS.md)**.
Amendment log for anything the spec didn't cover: **[`docs/SPEC_AMENDMENTS.md`](docs/SPEC_AMENDMENTS.md)**.

## Status

**Currently:** Phases 0–1 landed. The engine (`.janusignore`/`.janusmask` discovery, resolution, precedence, fail-closed folding), the built-in pattern library, and the static linter (`janusfs check`) plus the per-file tracer (`janusfs explain`) all work today against a real directory tree — no mount required. The FUSE-T mount adapter itself (Phase 0's walking skeleton) is present but Phase 2–4 (masking on the read path, hot reload, dashboard/API/history) are not yet wired up end-to-end.

Roadmap:

- [x] Phase 0 — walking-skeleton FUSE-T passthrough
- [x] Phase 1 — rule engine, three-state resolution, `janusfs init`/`check`/`explain`
- [ ] Phase 2 — pattern-based masking on the read path
- [ ] Phase 3 — hot reload, watcher, race-tight leak oracle
- [ ] Phase 4 — dashboard, history, HTTP API, per-mount token auth
- [ ] Phase 5 — diagnostics maturity (`janusfs doctor`, conflicts.json)

MVP is Phases 0–4.

## License

TBD. Please open an issue if you want to use JanusFS in production before this is decided.
