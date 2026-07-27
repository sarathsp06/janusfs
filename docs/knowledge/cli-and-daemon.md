---
type: Subsystem
title: CLI and daemon
description: The command tree, the control-socket protocol, mount lifecycle, resume, and the crash-recovery paths that exist today.
tags: [cli, daemon, ipc, lifecycle, recovery]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: main
    resource: /cmd/janusfs/main.go
    title: root command wiring
  - id: daemon
    resource: /cmd/janusfs/daemon.go
    title: daemon, protocol handlers, resume
  - id: mountcmd
    resource: /cmd/janusfs/mount.go
    title: mount and update commands
  - id: umount
    resource: /cmd/janusfs/umount.go
    title: unmount ladders, direct unmount
  - id: pidfile
    resource: /cmd/janusfs/pidfile.go
    title: pidfiles, pidAlive, mirror-dir pruning
---

# Command tree

Registered in `newRootCmd` (`cmd/janusfs/main.go:48`). Every command except
`daemon` is a short-lived socket client.

| Command | Purpose | Flags |
|---|---|---|
| `daemon` | run the long-lived owner of all mounts | `--debug`, `--no-open`, `--ui-port` |
| `mount <src> [mountpoint]` | ask the daemon to mount | `--name`, `--no-history` |
| `update [src\|mountpoint\|configpath]` | recompile rules for one or all mounts | — |
| `umount <mountpoint\|src>` | unmount via daemon, or directly if none is running | — |
| `install` | one-time setup: choose and save a mount root | `--root`, `--global-rules` |
| `init [dir]` | write template `.janusignore`/`.janusmask` | `--global`, `--force` |
| `paths` | list config/data paths and whether each exists | — |
| `path <src>` | print the mountpoint for a mounted source, for `cd`/scripting | — |
| `check [path]` | static config linter | `--json` |
| `explain <path>` | show the derivation of one path's decision | — |
| `doctor` | runtime diagnostics | `--verbose` |
| `exec -- <cmd>` | run a command against a sanitized view | flag parsing disabled |
| `watchdog` | supervise a daemon PID, force-unmount its mounts if it dies | `--pid`, `--interval` — hidden, spawned by `daemon`, not for manual use |

`main` sets `SilenceErrors` and `SilenceUsage` and prints a single
`janusfs: <cause>` line itself (`main.go:34`). An error whose message is empty
means the command already printed its own user-facing report and only needs the
non-zero exit code — that is how `check` reports findings without a redundant
prefix line.

# Control protocol

One JSON object per connection, request then response, over
`~/.janusfs/daemon.sock`. The protocol types (`Request`, `Response`,
`MountStatus`) and the dial-and-call helper (`Call`) live in one place,
`internal/control` (`internal/control/control.go`), imported by both
`cmd/janusfs` and `internal/execrunner`. This used to be two independent
copies — one unexported in `package main`, one duplicated in
`internal/execrunner` because it couldn't reach the unexported original — which
could drift silently; that gap is closed (was
[known gaps](known-gaps.md) item 8, now removed from that register).

`cmd/janusfs/daemon.go` keeps the historical local names `daemonRequest`,
`daemonResponse`, `mountStatus` as **type aliases** to `control.Request`,
`control.Response`, `control.MountStatus`, so none of that package's many call
sites needed to change:

```go
type (
    daemonRequest  = control.Request
    daemonResponse = control.Response
    mountStatus    = control.MountStatus
)
```

```go
type Request struct {
    Cmd        string // "mount" | "unmount" | "list" | "reload"
    Src        string // mount: source tree
    Mountpoint string // unmount: mountpoint or src; reload: any path inside either
    Label      string // mount: dashboard label, not a path
    NoHistory  bool
    Resume     bool   // json:"-" — internal only, set by daemon startup
}

type Response struct {
    OK      bool
    Error   string
    Message string
    Mounts  []MountStatus // {Src, Label, Mountpoint, Dashboard}
}
```

`control.WriteResponse` sets `OK = OK || Error == ""`, so a handler that
returns a bare `Response{}` is reported as success. `Resume` is `json:"-"`
deliberately: a client cannot ask the daemon to treat its request as a resume,
because resume is allowed to create a recorded mountpoint directory that a
fresh mount is not.

# Mount lifecycle

`doMount` (`daemon.go:284`) holds `opsMu` across the entire
check-resolve-start-insert sequence, so two concurrent mounts of the same
mountpoint cannot both pass the "already mounted" guard and leak a runtime.
There are two mutexes on purpose: `opsMu` serializes operations, `mu` guards the
map for concurrent reads.

Ordered steps:

1. `cfg.ResolveMountpoint()` derives the mountpoint when the client omitted it.
2. On resume only, `MkdirAll` the recorded mountpoint.
3. Reject if the absolute mountpoint is already in the map.
4. `validateMountConfig(cfg)` → `config.Validate()`.
5. **Stale-mount retry**: if validation failed with `syscall.ENXIO` — the
   signature of a broken FUSE mountpoint whose server is gone — force-unmount
   it, recreate the directory, and retry validation once (`daemon.go:324`).
   This is the one automatic crash-recovery path that exists today.
6. `startMount` → `mountRuntime`.
7. Write the pidfile, record the mount in the registry.

Validation failures are translated into actionable text by
`mountValidationError` (`daemon.go:363`): a non-empty mountpoint that has other
JanusFS mounts nested under it names them and tells the user to unmount those
first, because a parent mount would shadow them.

## Resume

`resume()` (`daemon.go:435`) replays every record in
`~/.janusfs/mounts.json` at daemon start, before the control socket begins
accepting connections. One bad record logs a warning and does not stop the
rest. This is what makes mounts survive a daemon restart or a reboot.

## Unmount

`doUnmount` (`daemon.go:378`) matches on the mountpoint first, then falls back
to matching the source path, because the derived mountpoints are deep and
unmounting by source is friendlier. If the target is not live in this daemon it
tries `pruneStaleRegistry` (`daemon.go:488`) and reports honestly that it
removed a stale entry rather than returning an unactionable "not mounted".

The client side, `runUmount` (`cmd/janusfs/umount.go:32`), has three paths:

- no daemon → `directUnmount`, so a mount left behind by a crashed daemon can
  still be cleaned up;
- daemon succeeded → additionally check whether anything is *still* mounted at
  the requested path or any returned registry mountpoint, and clear it quietly;
- daemon up but does not know the path → if something is nonetheless mounted
  there, clean it directly.

`isMountpoint` (`umount.go:85`) compares a path's device number with its
parent's — a cheap check that avoids invoking `diskutil` where nothing is
mounted.

`directUnmount` (`umount.go:99`) force-unmounts, then `SIGTERM`s the pidfile
owner if there is one, removes the pidfile, and clears the registry entry.

# Unmount ladders

`unmountKernel(mountpoint, force)` (`umount.go:126`) dispatches on
`runtime.GOOS` through package-level variables (`unmountCommand`,
`runtimeGOOS`, `mountpointMounted`, `umount.go:116`) that tests override.

- **darwin** (`:133`): `diskutil unmount` → `umount` → and with force,
  `diskutil unmount force`.
- **linux** (`:160`): `fusermount3 -u` → `fusermount -u` → `umount` → and with
  force, `fusermount3 -uz` → `fusermount -uz` → `umount -l`. The lazy variants
  are what detach a stale mountpoint reporting "Transport endpoint is not
  connected" without needing `diskutil`.

Every attempt runs under a 5-second timeout with the process killed on expiry
(`tryUnmount`, `umount.go:186`). All failures are collected and joined, so the
user sees every attempt rather than only the last.

# Pidfiles

`~/.janusfs/run/<sha256-of-abs-mountpoint>.pid`, mode `0600`, directory `0700`
(`cmd/janusfs/pidfile.go:18`). The daemon writes its **own** PID for each
mountpoint (`daemon.go:350`), so the file identifies the owning process, not a
per-mount process.

The file carries two lines: the PID on the first, the absolute mountpoint on
the second (`writePidfile`, `pidfile.go:36`). The mountpoint line exists
because the filename itself is a one-way SHA-256 hash — with only the PID
recorded, nothing could recover which mountpoint a stale pidfile belonged to,
which is exactly the gap that made `janusfs doctor` unable to report a real
path (was [known gaps](known-gaps.md) item 9, now closed).
`readPidfile` (`pidfile.go:50`) parses only the first line, so a pidfile
written by an older build (PID only, no second line) still reads correctly;
`readPidfileMountpoint` (`pidfile.go`) reads the second line given a pidfile
path, returning `""` for a legacy single-line file.

`pidAlive` (`pidfile.go:94`) uses the POSIX signal-0 existence probe and counts
`EPERM` — process exists but is owned by another user — as alive, because that
is a real collision either way.

`health.Run` reconstructs mount info from this directory and warns about stale
pidfiles (`internal/health/doctor.go:71`). `MountInfo.MountpointKnown` is `true`
when the pidfile's second line supplied a real path; when `false`,
`MountInfo.Mountpoint` falls back to the filename with `.pid` stripped — a
hash, not a path — and callers (`janusfs doctor`'s printer) must present it as
unknown rather than as an actionable location.

`pruneMirrorDirs` (`pidfile.go:72`) removes the now-empty mountpoint and its
empty parents, stopping at the mount root, guarded by a strict-prefix check so
it can only ever touch paths under that root.

# Crash recovery: the watchdog

A FUSE mount whose server has died does not fall back to the underlying
filesystem — the kernel keeps it attached and every process touching that path
hangs or gets `EIO`. `mountRuntime.stop()`'s force-unmount ladder only runs on a
graceful daemon shutdown, so `SIGKILL`, an OOM kill, or an escaping panic used
to leave every mount hung with no automatic recovery (was
[known gaps](known-gaps.md), now closed).

`spawnWatchdog` (`cmd/janusfs/watchdog.go`) is called once by `runDaemon`, after
the control socket is bound and before the accept loop starts: it re-execs the
janusfs binary as `janusfs watchdog --pid <this-daemon's-pid>`, detached via
`SysProcAttr{Setsid: true}` so a terminal `SIGINT` reaching the daemon's process
group doesn't also kill the watchdog in the same instant. The watchdog's own PID
is recorded on the `daemon` struct and written to `~/.janusfs/watchdog.pid`
(mode `0600`) — deliberately **outside** `~/.janusfs/run/`, since that directory
is scanned by `health.Run` treating every `*.pid` file as one mount; colocating
it there would make the watchdog show up as a phantom mount in `janusfs doctor`.
A spawn failure only logs a warning: a missing watchdog degrades crash recovery
to manual, and must never prevent the daemon itself from starting.

`runWatchdog` (`watchdog.go`) polls the supervised PID every `--interval`
(default 500 ms) with the same signal-0 existence probe as `pidAlive`, exiting
cleanly if its own context is cancelled (a courtesy `SIGTERM` sent by
`stopWatchdog` during a graceful `daemon.shutdown()`). Once the daemon is found
dead, `sweepStaleMounts` walks `~/.janusfs/mounts.json` and force-unmounts,
via the existing `unmountKernel` ladder, only the mountpoints `isMountpoint`
still reports as mounted.

That `isMountpoint` guard is the entire coordination protocol between an
ungraceful death and a graceful shutdown: a clean shutdown has already
unmounted everything before the daemon exits, so the watchdog's sweep in that
case finds nothing mounted and is a no-op — the same code path handles both
cases correctly with no lease file, handshake, or shutdown notification.

Verified by hand: killing a daemon with `kill -9` while it holds an active
mount restores native filesystem access to that path in well under a second
(watchdog poll interval is 500 ms), confirmed via the daemon's own structured
log (`watchdog spawned` → `watchdog started` → `daemon no longer alive` →
`force-unmounted`).

`janusfs doctor` reports watchdog presence and liveness
(`health.Report.Watchdog`, read from `~/.janusfs/watchdog.pid`); a daemon
running without one is a warning, not an error, since mounts still work fine —
only automatic crash recovery is unavailable.

# Dashboard multiplexing

One HTTP listener on `127.0.0.1:<ui-port>` serves everything
(`daemon.go:148`). `handleHTTP` (`:537`) routes `/` to a hand-rolled combined
index and `/mounts/<uuid>/...` to that mount's `api.Server` after stripping the
prefix from `r.URL.Path` (`:560`). Per-mount listeners were removed in favour
of this single port.

Each mount gets a `crypto/rand` 16-byte hex bearer token at startup
(`cmd/janusfs/runtime.go:88`).
