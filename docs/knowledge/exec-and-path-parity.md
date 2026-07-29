---
type: Architecture
title: Exec and the path-parity problem
description: What janusfs exec does on darwin, why it rewrites path strings there, why string rewriting cannot be made correct, and how Linux avoids the problem entirely via PRP 04's private mount namespace.
tags: [exec, path-parity, execrunner, limitation]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: runner
    resource: /internal/execrunner/runner.go
    title: Run, findSourceAndMount, CWD hijack, env scrub
  - id: rewriter
    resource: /internal/execrunner/rewriter.go
    title: ReplacePaths
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

**This is now a darwin-only problem.** On Linux, `janusfs exec` no longer
rewrites anything — [PRP 04](/PRPs/04-linux-namespace-exec.md) gives the child
process tree a private mount namespace, so the filtered view is mounted at the
source's own path and the mismatch above never exists in the first place. See
[platform isolation models](platform-isolation.md) for how, and the rest of
this document — everything below — for the darwin path, which is what
`internal/execrunner/runner.go` (build-tagged `darwin`) still implements.

# What exec does on darwin

`cmd/janusfs/exec.go` sets `DisableFlagParsing: true` so everything after
`exec` is captured verbatim, splits on the first `--`, and hands the rest to
`execrunner.Run`. If no `--` is present, all arguments are treated as the
command.

`execrunner.Run` (`internal/execrunner/runner.go:131`, `//go:build darwin`)
then does six things:

1. **Find or provision a mount.** `findSourceAndMount` (`:61`) asks the daemon
   for its mount list, then walks up from the cwd. If an ancestor is an active
   mount source, that pairing is used. Otherwise the shallowest ancestor
   containing a `.janusfs.yml` becomes the source, defaulting to
   the cwd, and a `mount` request is sent to provision it.
2. **Wait for readiness.** Poll for `<mountpoint>/.janusfs` every 50 ms up to
   2000 ms (`:147`). This is why the synthetic `.janusfs` directory is
   load-bearing.
3. **Hijack the working directory** (`:162`). The cwd's position relative to the
   source is computed and reapplied under the mountpoint, so
   `src/pkg/sub` becomes `mountpoint/pkg/sub`.
4. **Rewrite argv** (`:180`): every argument has occurrences of the source path
   replaced with the mountpoint via `ReplacePaths`.
5. **Scrub the environment** (`:186`): every `JANUSFS_*` variable is dropped, so
   the child cannot read or influence JanusFS configuration.
6. **Pass stdout and stderr through unchanged** (`:189`): output is not path
   rewritten. This keeps TTY semantics for interactive CLIs and makes stdout and
   stderr byte-faithful, at the cost that output may show the internal mountpoint.

Signals `SIGINT`, `SIGTERM`, `SIGHUP` are forwarded to the child, and the
child's exit code is propagated. A failure to start returns `125`, matching
`env`/`timeout` convention.

# The argv rewriter

`ReplacePaths` (`internal/execrunner/rewriter.go:18`) is a boundary-aware
substring replace used for argv only: a match only counts if the preceding byte
is not a path character and the following byte is `/` or not a name character.
That stops `/a/app` from matching inside `/a/application`.

JanusFS deliberately does not rewrite stdout or stderr. The former stream
rewriter broke interactive CLIs by hiding their terminal and made output non
byte-faithful for only cosmetic path names.

The remaining argv rewrite is still a best-effort compatibility shim, not path
parity.

# Why string rewriting cannot be made correct

The argv rewriter can only touch command-line arguments. Every other channel
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

The conclusion is not "improve the rewriter". It is that **path parity must be
provided by the filesystem, not simulated in a wrapper**. Once the sanitized
view is available at the same absolute path as the source, every step above
disappears: no cwd hijack, no argv rewrite, no readiness race. See
[platform isolation models](platform-isolation.md).

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
