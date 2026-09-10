---
type: Architecture
title: Exec and the path-parity problem
description: What janusfs exec does on darwin (advisory-only: cwd hijack and env scrub, argv passed verbatim), why the former argv rewriter was removed, and how Linux avoids the problem entirely via PRP 04's private mount namespace.
tags: [exec, path-parity, execrunner, limitation]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: runner
    resource: /internal/execrunner/runner.go
    title: Run, findSourceAndMount, CWD hijack, env scrub
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

The mountpoint is never the source path — `ResolveMountpoint` mirrors the
source's full path *under* the mount root, and `Validate` actively rejects any
overlap between the two (`internal/config/config.go:345` and `:418`) — so an
agent pointed at the sanitized view is working at a different absolute path than
the one its tools, its config files, and its own memory expect.

```
source:      /Users/me/projects/app
mountpoint:  /Users/me/.janusfs/mounts/Users/me/projects/app
```

`janusfs exec` exists to paper over that difference.

**This is now a darwin-only problem, and on darwin it is advisory-only.** On
Linux, `janusfs exec` needs no compensation at all — [PRP 04](/PRPs/04-linux-namespace-exec.md)
gives the child process tree a private mount namespace, so the filtered view is
mounted at the source's own path and the mismatch above never exists in the
first place. See [platform isolation models](platform-isolation.md) for how,
and the rest of this document — everything below — for the darwin path, which
is what `internal/execrunner/runner.go` (build-tagged `darwin`) still
implements.

# What exec does on darwin

`cmd/janusfs/exec.go` sets `DisableFlagParsing: true` so everything after
`exec` is captured verbatim, splits on the first `--`, and hands the rest to
`execrunner.Run`. If no `--` is present, all arguments are treated as the
command.

`execrunner.Run` (`internal/execrunner/runner.go:123`, `//go:build darwin`)
then does six things:

1. **Find or provision a mount.** `findSourceAndMount` (`:52`) asks the daemon
   for its mount list, then walks up from the cwd. If an ancestor is an active
   mount source, that pairing is used. Otherwise the shallowest ancestor
   containing a `.janusfs.yml` becomes the source, defaulting to
   the cwd, and a `mount` request is sent to provision it.
2. **Warn about git staging hazards** (`:138`). `warnGitStagingHazards`
   (`internal/execrunner/hazard.go`) calls `check.GitStagingHazards` and prints
   a loud stderr warning when Masked files under the source are stageable by
   git — `git add` through the filtered view stages the masked bytes, not the
   real content. Best-effort and advisory; a detection failure never blocks the
   exec.
3. **Wait for readiness.** Poll for `<mountpoint>/.janusfs` every 50 ms up to
   2000 ms (`:143`). This is why the synthetic `.janusfs` directory is
   load-bearing.
4. **Hijack the working directory** (`:155`). The cwd's position relative to the
   source is computed and reapplied under the mountpoint, so
   `src/pkg/sub` becomes `mountpoint/pkg/sub`.
5. **Scrub the environment** (`:173`): every `JANUSFS_*` variable is dropped, so
   the child cannot read or influence JanusFS configuration.
6. **Pass argv, stdout, and stderr through unchanged** (`:183`, `:191`):
   arguments reach the child verbatim — there is no argv rewriting — and
   output is not path rewritten. This keeps TTY semantics for interactive CLIs
   and makes stdout and stderr byte-faithful, at the cost that both arguments
   and output may name the internal mountpoint or the real source path.

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
killed the stream rewriter earlier: a shim that sometimes works is worse than
a boundary honestly described as advisory. macOS exec now passes argv
verbatim; what remains (cwd hijack, env scrub, git-hazard warning) is
explicitly advisory, not a parity mechanism.

# Why string rewriting could not be made correct

An argv rewriter can only touch command-line arguments. Every other channel
through which a path escapes is unreachable:

- **Terminal output.** JanusFS now leaves stdout and stderr untouched so tools
  can use the terminal normally; printed paths may therefore name the mountpoint.
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
`fuseintegration` CI job. On macOS there is no equivalent kernel mechanism,
and the compensating enforcement track — process identity, a path-preserving
overmount, and Seatbelt `--sandbox` confinement — was ultimately rejected and
deleted; see [platform isolation models](platform-isolation.md) and
[SPEC.md §20](/SPEC.md). macOS exec is advisory by design.

# Two defects, now fixed

**Exec used to ignore its own readiness failure mode.** If the daemon is not
running, `findSourceAndMount` returns a friendly error telling the user to
start it. But when the daemon *was* running and the cwd was an arbitrary
directory with no policy files, exec used to silently mount the **cwd itself**
as a source — which for a user who ran `janusfs exec` in their home directory
meant provisioning a mount over their entire home tree with no policy.
`findSourceAndMount` (`internal/execrunner/runner.go`) now refuses instead,
returning a cause-and-remedy error naming `janusfs init` — see
[PRP 01](/PRPs/01-correctness-fixes.md) task 3.

**Duplicate protocol types.** `daemonRequest`, `daemonResponse`, and
`mountStatus` used to be declared twice — once unexported in `package main`,
once duplicated in `internal/execrunner` — and could drift. Both now import the
shared types from `internal/control`; see
[CLI and daemon](cli-and-daemon.md)'s Control protocol section.
