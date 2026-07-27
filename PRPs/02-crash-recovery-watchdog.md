# PRP 02 — Crash recovery watchdog

**Size** S · **Blocked by** nothing · **Touches** `cmd/janusfs`

## Goal

After the daemon dies without running its shutdown path, every mounted path
returns to native filesystem access within 5 seconds, with no user action.

## Why

A FUSE mount whose server is gone does **not** fall back to the underlying
filesystem. The kernel keeps the mount attached and every process touching that
path blocks or gets `EIO`. For a developer that means their editor, their shell,
and their build all hang on the project directory, with nothing on screen
explaining why.

The existing force-unmount ladder in `mountRuntime.stop()`
(`cmd/janusfs/runtime.go:172`) only runs on the graceful path. `SIGKILL`, an OOM
kill, or a panic that escapes the recover in `resolve()` bypasses it entirely.
Recovery today is reactive and requires the user to already know
`janusfs umount`.

## Context

- Lifecycle and the existing recovery paths:
  [`docs/knowledge/cli-and-daemon.md`](../docs/knowledge/cli-and-daemon.md)
- Requirements: [SPEC.md FR-35, FR-36](../SPEC.md#36-reliability-and-recovery),
  [NFR-12](../SPEC.md#4-non-functional-requirements)
- Gap register item 3:
  [`known-gaps.md`](../docs/knowledge/known-gaps.md)

Everything this needs already exists. Reuse, do not reimplement:

| Need | Existing function |
|---|---|
| Is this path still mounted? | `isMountpoint(path) bool`, `cmd/janusfs/umount.go:85` — compares device number with parent's |
| Force-unmount, per platform | `unmountKernel(mountpoint string, force bool) error`, `cmd/janusfs/umount.go:126` — darwin `diskutil`/`umount` ladder, linux `fusermount3`/lazy ladder, each attempt timeout-bounded |
| Which mountpoints exist? | `config.LoadMounts() ([]config.MountRecord, error)`, `internal/config/config.go:228` |
| Is a PID alive? | `pidAlive(pid int) bool`, `cmd/janusfs/pidfile.go:94` — signal-0 probe, counts `EPERM` as alive |

## Design

One detached supervisor process per **daemon**, not per mount. A per-mount
supervisor would multiply processes for no benefit, since one daemon death takes
down every mount it owns simultaneously.

```
janusfs daemon starts
  └─ spawns: janusfs watchdog --pid <daemon-pid>     (detached, hidden command)
                    │
                    │  every 500ms: pidAlive(daemonPid)?
                    ▼
              daemon gone
                    │
                    ▼
        for each record in ~/.janusfs/mounts.json:
              if isMountpoint(record.Mountpoint):     ← the whole race fix
                    unmountKernel(mountpoint, force=true)
        exit 0
```

### Why the `isMountpoint` guard is the entire coordination protocol

A clean shutdown has already unmounted everything before the daemon exits. So
when the watchdog wakes and finds the daemon gone, its sweep finds nothing
mounted and does nothing. The same code path handles both a crash and a clean
exit, correctly, with no lease file, no handshake, no shutdown notification, and
no window in which the watchdog can unmount a mount that is still in use.

**Do not add coordination.** Any design where the daemon must tell the watchdog
"I exited cleanly" introduces the failure mode the watchdog exists to handle: if
the daemon dies before sending the message, the watchdog must act anyway, so the
message can never be trusted, so it is not worth sending.

## Blueprint

### Task 1 — `cmd/janusfs/watchdog.go`

```go
func newWatchdogCmd() *cobra.Command {
	var daemonPid int
	var interval time.Duration
	cmd := &cobra.Command{
		Use:    "watchdog",
		Short:  "Supervise the daemon and force-unmount its mounts if it dies",
		Hidden: true,   // spawned by the daemon, not invoked by hand
		Args:   cobra.NoArgs,
		RunE:   func(cmd *cobra.Command, _ []string) error {
			return runWatchdog(cmd.Context(), daemonPid, interval)
		},
	}
	cmd.Flags().IntVar(&daemonPid, "pid", 0, "PID of the daemon to supervise (required)")
	cmd.Flags().DurationVar(&interval, "interval", 500*time.Millisecond, "liveness poll interval")
	return cmd
}
```

`runWatchdog`:

- Reject `pid <= 0` with a clear error. Reject a pid that is not alive at start —
  there is nothing to supervise, and silently idling would be worse than saying
  so.
- Poll on a `time.Ticker`. Exit 0 as soon as the sweep completes.
- Honour context cancellation, so a `SIGTERM` to the watchdog exits without
  sweeping. This is what lets the daemon kill its own watchdog during a clean
  shutdown as a belt-and-braces measure.
- Log through `logging.New("watchdog")` to stderr, at info: one line on start
  (which pid, which interval), one line per unmounted path, one on exit. A user
  reading the daemon's log after a crash should be able to see that recovery
  happened.

`Hidden: true` because a user typing `janusfs watchdog` by hand cannot supply a
meaningful `--pid` and would only confuse themselves. It stays a real command so
it is testable and debuggable.

### Task 2 — Spawn it from the daemon

In `runDaemon` (`cmd/janusfs/daemon.go:93`), after the control socket is bound and
before the accept loop, spawn the watchdog detached:

```go
exe, err := os.Executable()
// ... on error: log a warning and continue. A missing watchdog degrades
// recovery; it must never prevent the daemon from starting.
wd := exec.Command(exe, "watchdog", "--pid", strconv.Itoa(os.Getpid()))
wd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}   // survive the daemon's process group
wd.Stdout, wd.Stderr = os.Stderr, os.Stderr
_ = wd.Start()
```

`Setsid: true` matters: without it a terminal `SIGINT` reaches the watchdog at
the same moment it reaches the daemon, and the watchdog dies before it can
observe the death it exists to observe.

Record the watchdog's PID on the `daemon` struct. In `d.shutdown()`
(`daemon.go:451`), after all mounts are stopped, signal it `SIGTERM`. That is an
optimisation, not a correctness requirement — the `isMountpoint` guard already
makes a surviving watchdog harmless.

### Task 3 — Surface it in `doctor`

`janusfs doctor` reports whether a watchdog is supervising the running daemon.
Add a `Watchdog` field to `health.Report` (`internal/health/doctor.go:17`)
carrying the PID and liveness. Discovering it: the daemon writes the watchdog PID
to `~/.janusfs/run/watchdog.pid` (mode `0600`) at spawn and removes it on clean
shutdown.

A daemon running without a supervisor is a warning, not an error: mounts work
fine, recovery is just manual.

## Validation

```bash
rtk make verify
```

Unit tests, `cmd/janusfs/watchdog_test.go` — no real mount needed, because the
package-level indirection variables already exist for exactly this:

```go
var (
	unmountCommand    = tryUnmount      // umount.go:117
	runtimeGOOS       = runtime.GOOS
	mountpointMounted = isMountpoint
)
```

Override `mountpointMounted` and `unmountCommand` in tests to record calls.

- **sweeps only mounted paths**: registry with three records, `mountpointMounted`
  true for one — assert exactly one `unmountCommand` call, for that path.
- **clean shutdown is a no-op**: `mountpointMounted` always false — assert zero
  unmount calls.
- **exits on cancel without sweeping**: cancel the context while the supervised
  pid is still alive — assert zero unmount calls.
- **refuses a dead or invalid pid**: assert an error, not a silent idle.

Manual verification, and it is worth doing by hand once because the failure mode
is a hung terminal:

```bash
janusfs daemon &                       # note the PID
janusfs mount ~/projects/app
kill -9 <daemon-pid>                   # the ungraceful path
# within 5s:
mount | grep janusfs                   # must be empty
ls ~/.janusfs/mounts/Users/...         # must not hang
```

## Done when

- [ ] `janusfs watchdog --pid N` exists, hidden, and force-unmounts only paths
      that are still mounted
- [ ] The daemon spawns it detached with `Setsid`, and a spawn failure only warns
- [ ] `kill -9` on a daemon with two live mounts restores native access to both
      within 5 s, verified by hand
- [ ] A clean `SIGTERM` shutdown produces zero unmount calls from the watchdog
- [ ] `janusfs doctor` shows watchdog status, warning when absent
- [ ] Item 3 removed from [`known-gaps.md`](../docs/knowledge/known-gaps.md); a
      line appended to [`log.md`](../docs/knowledge/log.md)

## If this is wrong

- Assumes `isMountpoint`'s device-number comparison still returns true for a
  mount whose server has died. It should — the mount is still attached, which is
  the entire problem — but if `Lstat` on the hung path *blocks*, the watchdog
  wedges exactly when it is needed. Verify this specifically during the manual
  test. If it blocks, the fix is to do the sweep with a bounded timeout per path
  and fall through to `unmountKernel` unconditionally on timeout, since an
  unresponsive path is itself evidence the mount needs clearing.

## Anti-patterns

- Do not build a separate `janus-watchdog` binary. It would duplicate
  `unmountKernel` and `config.LoadMounts` for nothing.
- Do not add a lease file, heartbeat, or clean-exit notification.
- Do not have the watchdog restart the daemon. Recovery means restoring the
  user's filesystem, not resurrecting a process that just crashed for an unknown
  reason.
- Do not make the watchdog own or read policy. It force-unmounts paths; that is
  all it does.
