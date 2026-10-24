# PRP 20 — `janusfs exec` usability: make the boundary visible

**Size:** M · **Blocked by:** nothing · **Status:** done

## Why

`janusfs exec` is the product: a kernel-enforced, secret-masked view at the
project's own path, with optional network deny-all. It was also invisible.
Running `janusfs exec -- aider` looked and felt exactly like running `aider`:
no indication the boundary was on, what tree it covered, whether the network
was cut, or what it had done by the time the session ended. The only way to
preview a policy was to read the YAML and reason about globs; the only way
to find out a host could not enforce was to launch and read a namespace
error. A new user on a Mac could not evaluate the tool at all before
provisioning a Linux box.

Every item below is feedback or a default, never a change to enforcement.
That constraint is what let this ship as one PR: nothing here can make the
boundary weaker, and nothing here can make the operator's terminal a leak
channel (paths and counts only — see `docs/THREAT_MODEL.md`).

## What shipped

| Idea | Where | What the user sees |
|---|---|---|
| **Start banner** | `execrunner.WriteBanner` (`internal/execrunner/banner.go`), called from Stage 1 | `janusfs exec: boundary up — /home/me/app (policy .janusfs.yml)` + `network: none (loopback only) · from .janusfs.yml exec.net · preview with …` on stderr |
| **End-of-session summary** | `execrunner.SessionStats` (`stats.go`), fed by `mount.Adapter.Observe` in Stage 2 (`cmd/janusfs/nsmount_linux.go`) | `janusfs exec: session over (exit 0) — 2 masked file(s) read (.env, config/app.yml), 1 hidden path(s) denied (id_rsa) · 1204 fs op(s)` |
| **`--dry-run` (`--json`)** | `execrunner.Discover` + `Inspect` (`discover.go`, `inspect.go`), printed by `cmd/janusfs/dryrun.go` | The plan: tree, policy, cwd, network mode and its source, whether this host can enforce (with doctor's remedy if not), every Hidden/Masked file with its rule reference and patterns, git-stageable masked files, policy errors. Runs nothing; works on every OS |
| **`janusfs shell`** | `cmd/janusfs/shell.go` | `$SHELL` (or `/bin/sh`) inside the boundary, same flags as `exec` |
| **`JANUSFS_EXEC=1`** | Stage 2 child env | A prompt can show `[janusfs]`; scripts can refuse to run outside the boundary. The one deliberate exception to the env scrub |
| **`--quiet`/`-q`** | `Options.Quiet`, forwarded to Stage 2 | No banner, no summary. Security warnings still print |
| **Per-project `exec.net`** | `rules.ReadExecPolicy` (`internal/rules/execpolicy.go`), `resolveNetMode` (`cmd/janusfs/exec.go`) | `exec:\n  net: none` in `.janusfs.yml` makes every exec in that repo offline by default. Tighten-only; `--net` flag wins with a note |
| **Flags without `--`** | `splitExecArgs` | `janusfs exec --net=none aider` works; `--` still wins when present |
| **`doctor` readiness line** | `health.ExecStatus`, `cmd/janusfs/doctor.go` | `✓ Exec sandbox: ready` or `✗ Exec sandbox: unavailable — <cause>` + remedy, second line of the report |
| **`init` template** | `cmd/janusfs/init.go` | The commented `exec:` block, so the feature is discoverable where policies are written |

## Precedence of the network mode

Lowest to highest: built-in `host` → `~/.janusfs/settings.json` `exec_net` →
`JANUSFS_EXEC_NET` → the project root's `.janusfs.yml` `exec.net`
(**tighten-only**: `none` applies, `host` never loosens an earlier `none`) →
`--net` flag (wins; a note is printed when it loosens the project's ask). An
invalid value from any source is refused in `resolveNetMode`, in one place.

## Considered and not built (recorded in SPEC §20)

- **A shell-prompt integration shipped by JanusFS.** Every shell differs; a
  single marker variable lets a user do it in one line of their own rc.
- **A persistent exec session log.** The daemon's history store exists for
  persistent mounts; `exec` is ephemeral by design and its summary is one
  line on the terminal that launched it.
- **Reading `exec.net` from subdirectory or global policy files.** `exec` has
  exactly one boundary per run: the root. Global defaults already have a home
  in `settings.json`.
- **A richer "why was this denied" inside the view.** The kernel returns
  `EACCES`; the agent-facing answer is `janusfs explain <path>` from outside.

## Conventions checked against the field

A survey of comparable tools (bubblewrap, firejail, nsjail, systemd-run,
docker/podman, nix-shell, direnv, toolbox/distrobox, venv, tmux, git,
Claude Code and Codex settings precedence) informed the final shape:

- **Inside marker as an env var** — `VIRTUAL_ENV`, `IN_NIX_SHELL`, `TMUX`,
  distrobox's `CONTAINER_ID`; `nix shell`'s lack of one is a standing
  complaint. Hence `JANUSFS_EXEC=1`, and no PS1 rewriting (Nix found that
  overwriting the prompt destroys user config).
- **No command → `$SHELL`** — firejail, unshare, chroot, `distrobox enter`.
  Kept as an explicit `shell` subcommand, like `systemd-run --shell`, since
  `exec` with no command refusing is the safer default for scripts.
- **Silent on success, diagnostics on stderr only** — bwrap, docker, landrun;
  firejail's "Reading profile…" banner and stdout pollution are the
  anti-patterns people file issues about. Hence two lines on stderr, a
  `--quiet` that never hides security warnings, and the Stage 2 `mounted`
  log line demoted to debug.
- **`--dry-run` prints the real plan** — `distrobox enter --dry-run` prints
  the generated podman command; bwrap grew `--json-status-fd` because callers
  could not otherwise tell what happened. Hence `--dry-run --json`.
- **Exit codes 125/126/127** — chroot, docker/podman, timeout. Added.
- **Project config can only tighten** — git honours `safe.directory` only
  from system/global config, never the repo; Codex's `requirements.toml`
  and Claude Code's managed settings cannot be lowered by a project. Hence
  `exec.net` is tighten-only, so a cloned repo needs no trust prompt.
- **Nesting named, not refused** — tmux warns via `$TMUX`. Nested exec is
  useful here (inner `--net=none`), so the banner says "nested".
- **Doctor: one line per check, ✓/✗ plus a remedy** — `flutter doctor`.

## Validation

```bash
rtk make verify                      # fmt-check vet test-race — green on darwin
GOOS=linux go vet ./...              # the Linux-only paths compile and vet
GOOS=linux go vet -tags fuseintegration ./...
janusfs exec --dry-run               # on any OS: the plan, no namespace
janusfs doctor                       # second line: Exec sandbox ready / unavailable
```

On a Linux host with `/dev/fuse`: `janusfs shell` then `cat .env` prints
stars, `echo $JANUSFS_EXEC` prints `1`, and exiting prints the session line.
The Stage 2 changes (stats, marker, summary) were reviewed against the
existing code paths but authored on darwin; CI's `fuseintegration` job runs
the real re-exec.

## If this is wrong

- If `Observe` ever becomes hot enough that a mutex per op shows in
  `bench/BASELINE.md`, switch `SessionStats` to per-decision atomics plus a
  bounded path set; the summary format need not change.
- If a tool inside the view misbehaves on seeing `JANUSFS_EXEC`, the name is
  the only contract; nothing reads it back.
