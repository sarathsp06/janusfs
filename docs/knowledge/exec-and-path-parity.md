---
type: Architecture
title: Exec and the path-parity problem
description: What janusfs exec does on Linux (a private mount namespace that mounts the filtered view at the project's own path, so no path rewriting is needed), why it refuses off Linux, and why the historical argv/stream rewriters were removed.
tags: [exec, path-parity, execrunner, limitation]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: runner
    resource: /internal/execrunner/runner_linux.go
    title: Run, discoverSourceRoot, env scrub, namespace re-exec
  - id: hazard
    resource: /internal/execrunner/hazard.go
    title: warnGitStagingHazards
  - id: execcmd
    resource: /cmd/janusfs/exec.go
    title: exec cobra command
  - id: resolvemp
    resource: /internal/config/config.go
    title: ResolveMountpoint, Validate
---

# The problem in one sentence

When the **daemon** mounts a source, the mountpoint is never the source path —
`ResolveMountpoint` mirrors the source's full path *under* the mount root, and
`Validate` actively rejects any overlap between the two
(`internal/config/config.go:345` and `:418`) — so an agent pointed at that
sanitized view would be working at a different absolute path than the one its
tools, its config files, and its own memory expect.

```
source:      /home/me/projects/app
mountpoint:  /home/me/.janusfs/mounts/home/me/projects/app
```

`janusfs exec` exists to erase that difference — and on Linux it erases it
completely, at the filesystem layer, rather than papering over it.

**`janusfs exec` is Linux-only.** [PRP 04](/PRPs/04-linux-namespace-exec.md)
gives the child process tree a private mount namespace, so the filtered view is
mounted at the source's *own* path and the mismatch above never exists in the
first place — no cwd hijack, no argv rewriting, no stream rewriting. See
[platform isolation models](platform-isolation.md) for how. On any non-Linux OS
`execrunner.Run` (`runner_other.go`, `//go:build !linux`) refuses with
`ErrUnsupportedOS`; there is no advisory fallback, because a boundary that
cannot be enforced is worse than an honest refusal.

# Network isolation (`--net=none`, Linux)

`janusfs exec --net=none` runs the target with no network access at all — the
deny-all equivalent of a container's `--network none`. It is opt-in (`--net`
defaults to `host`, i.e. current behavior), Linux-only, and kernel-enforced.

How: Stage 1 (`internal/execrunner/runner_linux.go`) adds `CLONE_NEWNET` to the
`CLONE_NEWNS|CLONE_NEWUSER` clone it already performs, so the child gets its own
network namespace whose only interface is loopback, with no route to any
external host. That empty namespace *is* the enforcement. It costs no
privilege: the child is uid 0 in its own user namespace, so it may create the
netns. Stage 2 (`cmd/janusfs/nsmount_linux.go`, `bringLoopbackUp`) raises the
loopback interface, which starts down in a fresh netns, so tools that talk to
`127.0.0.1` keep working. The flag is threaded to Stage 2 as `--deny-network`.

`cmd/janusfs/exec.go` parses `--net` by hand (exec sets `DisableFlagParsing`),
accepting only `--net=host` and `--net=none`, and builds an
`execrunner.Options{DenyNetwork: ...}`. When `--net` is omitted the default
follows the normal config precedence — `JANUSFS_EXEC_NET`, then `exec_net` in
`~/.janusfs/settings.json`, then `host` — via the `Config.ExecNet` field; an
explicit `--net` always wins. The final value (from any source) is validated in
one place, so a bad `exec_net` in the settings file fails the same way a bad
`--net` does.

Off Linux, `janusfs exec` refuses entirely (`runner_other.go`), so `--net` is
moot there: there is no advisory network mode to misrepresent.

Deliberately *not* provided: an egress allowlist (reach some hosts, not
others). That needs a usermode network stack (slirp4netns/pasta) or host root
plus an nftables ruleset — a forbidden dependency reimplementing what the
agent's container already does. See [SPEC.md §20](/SPEC.md)'s decision entry.

# What exec does (Linux)

`cmd/janusfs/exec.go` sets `DisableFlagParsing: true` so everything after
`exec` is captured verbatim, splits on the first `--`, and hands the rest to
`execrunner.Run`. If no `--` is present, all arguments are treated as the
command.

`execrunner.Run` (`internal/execrunner/runner_linux.go`, `//go:build linux`)
then does the following:

1. **Preflight** (`nsexec.Supported()`). Refuse early if the kernel cannot give
   an unprivileged process the mount/user namespaces this depends on.
2. **Discover the source root.** `discoverSourceRoot` walks up from cwd for a
   directory containing `.janusfs.yml` and *refuses* rather than defaulting to
   cwd — defaulting would provision an unpoliced view over whatever directory
   happens to be current. Unlike the historical macOS path, this never talks to
   the daemon: on Linux `janusfs exec` needs no daemon and works with none
   running.
3. **Warn about git staging hazards.** `warnGitStagingHazards`
   (`internal/execrunner/hazard.go`) calls `check.GitStagingHazards` and prints
   a loud stderr warning when Masked files under the source are stageable by
   git — `git add` through the filtered view stages the masked bytes, not the
   real content. Best-effort and advisory; a detection failure never blocks the
   exec.
4. **Scrub the environment**: every `JANUSFS_*` variable is dropped, so the
   child cannot read or influence JanusFS configuration.
5. **Re-exec into a private namespace.** The janusfs binary re-execs itself as
   `janusfs __nsmount --src <src> [--deny-network] -- <command>` with
   `CLONE_NEWNS|CLONE_NEWUSER` (plus `CLONE_NEWNET` for `--net=none`). Stage 2
   (`cmd/janusfs/nsmount_linux.go`) runs inside those namespaces and mounts the
   filtered view over the source's own path. The child's cwd is passed through
   unchanged — the same absolute path is valid on both sides — and stdout/stderr
   inherit the real descriptors, because paths already match and need no
   rewriting.

Signals `SIGINT`, `SIGTERM`, `SIGHUP` are forwarded to the child, and the
child's exit code is propagated. A failure to start returns `125`, matching
`env`/`timeout` convention.

# The argv rewriter (removed)

`ReplacePaths` (formerly `internal/execrunner/rewriter.go`) was a
boundary-aware substring replace that rewrote source-path occurrences in argv
to the mountpoint. It was deleted together with the rest of the macOS
enforcement track — see [SPEC.md §20](/SPEC.md),
[PRP 12](/PRPs/12-delete-macos-enforcement-track.md), and
[PRP 13](/PRPs/13-delete-macos-argv-rewriter.md). The same reasoning had already
killed the stream rewriter earlier: a shim that sometimes works is worse than a
boundary the filesystem enforces honestly. macOS support was subsequently
removed entirely, so no argv/stream shim survives anywhere — the next section is
the record of why simulating path parity in a wrapper was the wrong approach in
the first place.

# Why string rewriting could not be made correct

An argv rewriter can only touch command-line arguments. Every other channel
through which a path escapes is unreachable:

- **Terminal output.** Printed paths would name the mountpoint, and JanusFS does
  not touch stdout/stderr.
- **Files the agent writes.** A generated `tsconfig.json`, a `.env.local`, a
  lockfile, a `compile_commands.json`, a coverage report, an editor workspace
  file — all get the mountpoint baked in, and stay wrong after the mount goes
  away.
- **The git index and git config.** `git` records absolute paths in worktree
  and submodule metadata.
- **Build caches.** `cargo`, `go build`, `ccache`, `tsc --incremental`, and
  `node_modules/.cache` all key on absolute paths, so a build done inside the
  mount is a cache miss outside it, and vice versa — and some tools store
  absolute paths in artefacts (debug info, source maps).
- **Anything the agent spawns that talks over a socket or writes a database.**
  A language server, a test runner daemon, a dev server: their paths are not on
  our stdout.
- **Path length and identity.** `mountpoint` is much longer than `src`, which
  can breach shebang and `sun_path` limits, and code that compares a path
  against a configured root will not match.

The conclusion was not "improve the rewriter". It is that **path parity must
be provided by the filesystem, not simulated in a wrapper**. Linux provides
exactly that via the private mount namespace, verified per-PR by the
`fuseintegration` CI job. No non-Linux OS has an equivalent kernel mechanism,
and the compensating enforcement track — process identity, a path-preserving
overmount, and Seatbelt `--sandbox` confinement — was rejected and deleted
before macOS support itself was removed; see
[platform isolation models](platform-isolation.md) and
[SPEC.md §20](/SPEC.md).

# Two defects, now fixed

**Exec used to ignore its own readiness failure mode.** The historical macOS
path, when the daemon was running and the cwd had no policy files, silently
mounted the **cwd itself** as a source — for a user who ran `janusfs exec` in
their home directory that meant provisioning a mount over their entire home tree
with no policy. The current Linux `discoverSourceRoot`
(`internal/execrunner/runner_linux.go`) refuses instead, returning a
cause-and-remedy error naming `janusfs init` — see
[PRP 01](/PRPs/01-correctness-fixes.md) task 3.

**Duplicate protocol types.** `daemonRequest`, `daemonResponse`, and
`mountStatus` used to be declared twice — once unexported in `package main`,
once duplicated in `internal/execrunner` — and could drift. Both now import the
shared types from `internal/control`; see
[CLI and daemon](cli-and-daemon.md)'s Control protocol section.
