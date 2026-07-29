# JanusFS — Technical Specification

JanusFS is a policy-enforcing filesystem for AI agents. It presents a filtered
view of a real project directory, deciding per path and per read whether the
caller gets the real bytes, a redacted copy, or nothing at all.

This document is the binding engineering contract. Every behaviour in the
codebase must trace to a requirement here.

`FR-`/`NFR-` identifiers exist for reviewers and for cross-references *within
this document and the knowledge bundle*. They are deliberately **not** cited from
Go comments: a comment should state the constraint the code cannot show, not a
document coordinate that goes stale the moment requirements are renumbered. Code
comments explain *why*; this document says *what must be true*.

**Read this alongside [`docs/knowledge/`](docs/knowledge/index.md)**, an OKF
knowledge bundle describing the system *as built*, with `file:line` anchors.
This document says what must be true; the bundle says what is currently true and
where. When they disagree, the bundle is describing a defect and it is recorded
in [`docs/knowledge/known-gaps.md`](docs/knowledge/known-gaps.md).

---

## Contents

- [Part I — Requirements](#part-i--requirements)
  - [1. Goals and non-goals](#1-goals-and-non-goals)
  - [2. Definitions](#2-definitions)
  - [3. Functional requirements](#3-functional-requirements)
  - [4. Non-functional requirements](#4-non-functional-requirements)
- [Part II — Architecture](#part-ii--architecture)
  - [5. Process and package layout](#5-process-and-package-layout)
  - [6. FUSE adapter](#6-fuse-adapter)
  - [7. Decision engine](#7-decision-engine)
  - [8. Masking pipeline](#8-masking-pipeline)
  - [9. Backing access layer](#9-backing-access-layer)
  - [10. Isolation engines](#10-isolation-engines)
  - [11. Process identity](#11-process-identity)
  - [12. Observability internals](#12-observability-internals)
  - [13. HTTP API, UI, and virtual files](#13-http-api-ui-and-virtual-files)
  - [14. Error-handling matrix](#14-error-handling-matrix)
  - [15. Security model](#15-security-model)
  - [16. Configuration, logging, and process wiring](#16-configuration-logging-and-process-wiring)
  - [17. History store](#17-history-store)
- [Part III — Delivery](#part-iii--delivery)
  - [18. Independence of requirements](#18-independence-of-requirements)
  - [19. Test strategy](#19-test-strategy)
  - [20. Risks and rejected designs](#20-risks-and-rejected-designs)
- [Part IV — Instructions for the implementing agent](#part-iv--instructions-for-the-implementing-agent)
  - [21. Ground rules](#21-ground-rules)
  - [22. Repository conventions](#22-repository-conventions)
  - [23. Definition of done](#23-definition-of-done)
  - [24. Working style](#24-working-style)

---

# Part I — Requirements

## 1. Goals and non-goals

### Goals

1. An agent pointed at a project directory can read the code it needs and cannot
   read the secrets it does not, enforced at the filesystem boundary rather than
   by asking the agent to behave.
2. Policy is expressed in files developers already understand — `.gitignore`
   syntax for hiding, a small line-oriented format for masking.
3. Real files are never modified. Redaction happens on the way out.
4. The protected view is available at the **same absolute path** as the real
   source, so tools, config files, caches, and the agent's own memory all agree
   about where things are.
5. Host developer tools pay no cost they did not ask for.
6. Failure is visible and recoverable. A crash must not leave a developer with a
   hung directory and no idea why.

### Non-goals

- Protecting content by inode. Decisions are per path (FR-11).
- Protecting against a local adversary with the ability to read the source
  directory directly. JanusFS filters a *view*; it is not a sandbox and does not
  claim to contain a hostile process that can bypass it by other means.
- Network policy, process policy, or syscall filtering of any kind.
- Multi-user or multi-tenant operation. One user, one daemon, local only.
- Encryption at rest.

## 2. Definitions

- **Source tree** — the real directory being protected. Trusted.
- **Mount point** — where the filtered view appears. In the disjoint model this
  differs from the source; in path-preserving mode it *is* the source path.
- **Decision** — exactly one of `ALLOWED`, `MASKED`, `HIDDEN`, resolved per path.
- **Redaction** — byte-length-preserving replacement of a matched span with `*`
  (0x2A). File size never changes.
- **Rule set** — the compiled, immutable result of discovering every
  `.janusfs.yml` applicable to a source tree, plus the global level.
- **Generation** — a monotonic counter identifying one compiled rule set.
  Reloading produces a new generation; anything keyed to a rule set carries it.
- **Level** — one directory's config files. Levels are ordered shallowest first,
  with the global level always first.
- **Global level** — `~/.janusfs/config`, machine-wide defaults, outside any
  source tree and therefore outside the agent's reach.
- **Poisoned** — a decision forced to `HIDDEN` by a config error rather than by a
  rule.
- **Agent session** — a process tree launched through `janusfs exec`, which is
  the unit of identity in path-preserving mode.
- **Path parity** — the property that the filtered view and the source occupy the
  same absolute path.

## 3. Functional requirements

### 3.1 Mount and lifecycle

- **FR-1** `janusfs mount <src> [mountpoint]` asks the daemon to mount a
  filtered view of `<src>` and returns immediately. `<src>` must exist and be a
  directory. When `[mountpoint]` is omitted and a mount root is configured, the
  mountpoint is **derived by mirroring the source's full, symlink-resolved
  absolute path under the mount root** — `mount /Users/me/projects/app` with root
  `~/.janusfs/mounts` mounts at `~/.janusfs/mounts/Users/me/projects/app` — and
  created `0700`. Every source therefore maps to a unique, predictable location
  and two sources never collide. `--name` sets a dashboard label only; it never
  changes the path. In the disjoint model the mountpoint must be an empty
  directory and must not overlap the source; violations abort before any FUSE
  call, with a cause and a remedy.

- **FR-2** A single long-lived `janusfs daemon` owns every live mount. All other
  subcommands are short-lived clients that send one JSON object over a Unix
  control socket at `~/.janusfs/daemon.sock` and exit. A second daemon refuses to
  start and reports the running one's mounts. `SIGINT`/`SIGTERM` unmounts
  everything cleanly, drains the HTTP server, and zeroes caches, within a 5 s
  grace window, then forces.

- **FR-3** `janusfs umount <mountpoint|src>` unmounts, accepting either the
  mountpoint or the source path. It works through the daemon when one is running
  and falls back to a direct OS-level unmount when not, so a mount left by a
  crashed daemon can always be cleared. A pidfile at
  `~/.janusfs/run/<sha256-of-mountpoint>.pid` records the owning process. The
  unmount ladder is platform-specific and every attempt is bounded by a timeout;
  all failures are reported, not just the last.

- **FR-4** A missing FUSE implementation fails the mount with an install
  hint. `janusfs doctor` reports FUSE presence and version: macFUSE on darwin,
  `/dev/fuse` and `fusermount` on Linux.

- **FR-5** Mounts recorded in `~/.janusfs/mounts.json` are resumed at daemon
  start, before the control socket accepts connections. One unresumable record
  logs a warning and does not prevent the others. Records whose source no longer
  exists, or whose mount cannot be recovered during resume, are pruned from the
  registry. An explicit `umount` removes the record so resume does not revive a
  mount the user stopped.

### 3.2 Decision semantics

- **FR-6** Every path resolves to exactly one Decision under strict precedence
  `HIDDEN > MASKED > ALLOWED`.

- **FR-7** **Fail closed.** Any error during rule discovery, parse, compile, or
  evaluation resolves the affected path to `HIDDEN` and emits an error event. A
  recovered panic in a FUSE handler resolves the same way.

- **FR-8** Behaviour matrix. This table is authoritative; the adapter implements
  exactly it.

| Operation | ALLOWED | MASKED | HIDDEN |
|---|---|---|---|
| `readdir` (name listed in parent) | yes | yes | yes |
| `lookup`, `getattr` | real attrs | real attrs, real size | real attrs, real size |
| `open(O_RDONLY)` | passthrough fd | virtual handle, direct I/O | **EACCES** |
| `read` | passthrough | redacted bytes | n/a, open denied |
| `open` write-intent, `create`, `truncate` | passthrough | **EACCES** | **EACCES** |
| `write` | passthrough | n/a, open denied | n/a, open denied |
| `unlink`, `chmod`, `chown`, `utimens` | passthrough | **EACCES** | **EACCES** |
| `rename` | only if source **and** destination are ALLOWED | **EACCES** | **EACCES** |
| `mkdir`, `rmdir` | passthrough | n/a, see FR-10 | **EACCES** |
| `symlink`, `mknod` | passthrough if the new name is ALLOWED | **EACCES** | **EACCES** |
| `link` | only if the **target inode's path** and the new name are both ALLOWED (FR-11) | **EACCES** | **EACCES** |
| `readlink` | passthrough, ENOENT if the target escapes the root | same | **EACCES** |
| `getxattr`, `listxattr` | passthrough | passthrough | **EACCES** |
| `setxattr`, `removexattr` | passthrough | **EACCES** | **EACCES** |
| `mmap`-backed reads | as `read` | as `read` | n/a |
| `ioctl` | **ENOSYS** | **ENOSYS** | **ENOSYS** |
| `statfs` | passthrough | passthrough | passthrough |

  `write` needs no interception of its own: a write can only reach the backing
  file through a handle, and a write-intent `open` on a non-`ALLOWED` path is
  already denied. This is correct by construction, and FR-24 covers the one
  window where it is not sufficient.

- **FR-9** A `HIDDEN` directory makes its whole subtree inaccessible. Its name is
  still listed in its parent's `readdir`, but `opendir` on it returns `EACCES`,
  and every descendant is `HIDDEN` regardless of deeper rules. A hidden ancestor
  cannot be re-allowed below it.

- **FR-10** Directories are never `MASKED`. A `.janusfs.yml` mask path matching
  a directory applies to the files within it, equivalent to `dir/**`.
  `janusfs check` flags directory-matching mask globs and reports the rewrite.

- **FR-11** Hard links. A Decision is per path, not per inode, so two links to
  one inode may carry different decisions. For links that **already exist on
  disk** this is accepted and documented: the user creates them and can hide or
  mask both paths. An agent **creating** a link through the mount is a different
  matter — it is an active attempt to escape a mask in one syscall — and is
  denied unless the target inode's own path resolves `ALLOWED`.

- **FR-12** Symlinks whose target resolves inside the mount are decided by the
  target path's own rules at access time. A symlink whose target resolves outside
  the source tree is served as dangling (`ENOENT` on follow), so the view can
  never become an escape hatch to an unprotected path.

- **FR-13** **Policy evaluation must agree with the filesystem about path
  identity.** Where the backing filesystem treats two spellings of a name as the
  same file, the rule matcher must too. Concretely: on a case-insensitive volume
  — the default for APFS and HFS+ — `cat .ENV` opens the file `.env`, so glob
  matching must be case-insensitive there, or the mask is trivially evaded. The
  behaviour follows the volume's actual case sensitivity, not a global
  preference, because `!`-negation lines widen visibility and a blanket
  case-insensitive mode is therefore not uniformly fail-closed.

### 3.3 Configuration

- **FR-14** `.janusfs.yml` is the single policy file. Version 1 has three
  top-level rule sections:

  ```yaml
  version: 1
  hide: ["*.pem"]
  allow: ["public.pem"]
  mask:
    - paths: ["*.env"]
      patterns: [env-value]
  ```

  `hide` and `allow` use gitignore-style glob semantics: `**`, trailing-`/`
  directory patterns, and escaping. `allow` is the explicit form of gitignore
  negation. Later matches win, deeper levels are later, subject to FR-9 and
  FR-17.

- **FR-15** `.janusfs.yml` `mask` rules list file globs under `paths` and
  optional pattern references under `patterns`:

  ```yaml
  mask:
    - paths: ["*.env", "config/*.yaml"]
      patterns: [env-value, db-uri]
    - paths: ["secrets/*"] # no patterns means whole-file
  ```

  A pattern is either a builtin name or a `/RE2-regex/` custom regex. No
  `patterns` means `whole-file`. Multiple mask rules for one glob accumulate
  patterns as a set union. Custom regexes compile with Go `regexp` (RE2).
  **A compile failure fails that level's whole rule set closed** — every path
  its globs would have touched becomes `HIDDEN`, with a config-error event — and
  other levels are unaffected.

- **FR-16** The masked span is capture group 1 when the regex defines at least
  one group, otherwise the whole match. The replacement is `*` repeated for the
  span's byte length. Overlapping matches from multiple patterns are unioned: a
  byte masked by any pattern stays masked.

- **FR-17** Discovery is hierarchical. For a path `P`, config files are read from
  the global level (`~/.janusfs/config`) and from the source root down to
  `dir(P)`. **The global level is a fail-closed floor**: no in-tree rule may
  negate a `HIDDEN` verdict the global level set. Negation still works normally
  within the in-tree tier, and within the global level itself. A blocked negation
  is recorded in the trace rather than silently dropped, so `check` and `explain`
  can show the user their `!` line was ignored and why. Config files are
  `ALLOWED`, read-only through the mount, and **never writable** — `EACCES` on
  any write, checked before the policy lookup — so an agent cannot weaken the
  policy that governs it.

- **FR-18** Built-in pattern library. Names are reserved; a user regex may not
  shadow a builtin name.

| Name | Definition (RE2, in the noted mode) |
|---|---|
| `env-value` | line mode: `(?m)^\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.+?)\s*$` → group 1 |
| `aws-key` | `\b((?:AKIA\|ASIA\|ABIA\|ACCA)[0-9A-Z]{16})\b` and `(?i)\baws_secret_access_key\b\s*[=:]\s*([A-Za-z0-9/+=]{40})` |
| `private-key` | `(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`, whole match |
| `jwt` | `\b(eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})\b` |
| `db-uri` | `\b[a-z][a-z0-9+.-]*://([^\s:@/]+:[^\s@/]+)@` → group 1 |
| `github-token` | `\b((?:ghp\|gho\|ghu\|ghs\|ghr)_[A-Za-z0-9]{36,255})\b` |
| `generic-secret` | `(?im)\b(?:password\|passwd\|secret\|token\|api[_-]?key)\b\s*[:=]\s*["']?([^\s"']{6,})` → group 1 |
| `whole-file` | sentinel: mask every byte |

  Each is unit-tested against a fixture corpus of true positives and known
  false-positive traps. A change to a builtin bumps a `patterns_version` reported
  by `doctor` and the dashboard.

- **FR-19** `janusfs init [dir]` writes template `.janusfs.yml` to `dir`
  (default cwd); `--global` writes it to `~/.janusfs/config`. It
  refuses to overwrite without `--force`, and explains what it wrote and why in
  at most ten lines.

### 3.4 Reload and consistency

- **FR-20** There is **no file watcher**. Rule changes are applied on demand: via
  `janusfs update [src|mountpoint|configpath]`, the dashboard's reload button, a
  config save through the dashboard editor, or — for in-tree rule files only —
  automatically, the next time `open`/`opendir` resolves a path whose ancestor
  chain's `.janusfs.yml` set has changed on disk since the loaded generation was
  compiled (FR-20a). A recompile builds a full new snapshot
  off-thread and swaps it atomically; filesystem operations are never blocked by
  recompilation, and the previous generation serves until the swap. The
  rationale for having no *continuous* watcher: on macOS a per-directory watch
  of a large tree exhausted the descriptor limit, and cgo FSEvents bindings are
  forbidden (§21). FR-22 is what makes this safe.

- **FR-20a** The on-demand check in FR-20 is bounded by path **depth**, not tree
  size: on `open`/`opendir`, stat both config filenames at every ancestor
  directory between the resolved path and the mount root (a handful of
  `stat(2)` calls, never a directory walk) and compare existence+mtime against
  a snapshot taken at the last successful discovery/reload. Detects an edited
  file (mtime changed) and a brand-new file in a previously bare directory
  (existence changed) equally — the latter is the case a naive
  known-files-only mtime diff would miss. Never runs on a read handler; every
  read already re-resolves its decision against whichever generation is
  current (FR-22/FR-24), so a reload triggered here takes effect for
  already-open handles for free on their next read. Does not cover the global
  level (`~/.janusfs/config`, not an ancestor of any in-tree path) or a path
  nothing has opened since the edit — both still require `janusfs update`.

- **FR-21** A generation swap invalidates the decision cache and every cached
  redaction. Conservatively, all entries.

- **FR-22** **The read-time key is authoritative.** Every masked read validates
  `(path, mtime, size, inode, generation)` against the cache key before serving,
  re-stat'ing the real file on each read rather than trusting a key captured at
  open time. With no watcher this is the *only* change detector for data files,
  and it always was the source of truth.

- **FR-23** During a re-redaction, concurrent readers are served the *previous*
  redacted bytes when the pattern set is unchanged, and otherwise block until the
  rebuild completes, bounded at 10 s and then `EIO`. Raw source bytes are never
  served for a masked path under any interleaving. A rebuild that completes
  against a different pattern set than the caller needs fails closed rather than
  serving mismatched content.

- **FR-24** A policy reload must apply to handles that are already open. A file
  opened while `ALLOWED` must not continue serving real bytes after a reload
  makes it `MASKED` or `HIDDEN`. Tightening policy is precisely when the user
  expects the tightening to take effect.

### 3.5 Isolation and path parity

- **FR-25** `janusfs exec -- <command> [args...]` runs a command against a
  filtered view of the project tree containing the current directory. It is the
  only sanctioned way to launch an agent, because it is where an agent session is
  established. It forwards `SIGINT`, `SIGTERM`, and `SIGHUP`, propagates the
  child's exit code, and exits `125` when it cannot start the child.

- **FR-26** `exec` **refuses to guess**. If neither an active mount nor a
  directory containing `.janusfs.yml` is found at or above the
  current directory, it fails with a cause and a remedy (`janusfs init` here or
  in an ancestor). It must never default to mounting the current directory: for a
  user in their home directory that would provision an unpoliced mount over
  their whole home tree, which is the opposite of the tool's purpose.

- **FR-27** `exec` strips every `JANUSFS_*` variable from the child's
  environment, so the child cannot read or influence JanusFS configuration. The
  agent-session token (FR-32) is the single deliberate exception; it is an
  identifier that grants nothing and whose possession results in *more*
  restriction, not less.

- **FR-28** **Linux: private mount namespace.** `exec` creates a new mount and
  user namespace for the child process tree and mounts the filtered view over the
  source directory *inside that namespace*. The child therefore sees the filtered
  view at the source's own path — full path parity — while every process outside
  the namespace continues reading the real filesystem directly, at native speed,
  never entering FUSE. The requirements this imposes:

  1. The namespace is created at `clone` time on a re-exec of the janusfs binary,
     because `unshare(CLONE_NEWNS)` affects only the calling thread and the Go
     runtime migrates goroutines across threads.
  2. `CLONE_NEWUSER` accompanies `CLONE_NEWNS`, since an unprivileged process
     cannot otherwise obtain the capability to mount. A single uid and gid
     mapping to the invoking user is sufficient.
  3. **The mount tree is made recursively private before anything is mounted.**
     A new namespace inherits the parent's mounts as shared on most
     distributions, so without this the FUSE mount propagates back to the host
     and appears over the user's real project directory. Omitting this defeats
     the entire model and must be covered by a test that asserts the host mount
     table is unchanged.
  4. The FUSE server for that namespace is the `exec` process itself. The mount's
     lifetime is exactly the agent session's lifetime, and no cross-namespace
     descriptor plumbing is required. When the process tree exits, the namespace
     and its mount are reaped by the kernel with no host-visible effect.
  5. Registration with a running daemon is best-effort and for observability
     only. `janusfs exec` must work with no daemon running.

- **FR-29** With FR-28 in place, path rewriting is prohibited on Linux. The
  working-directory hijack and argv rewriting are removed, not merely bypassed.
  String rewriting cannot be made correct — it
  cannot reach files the agent writes, the git index, build caches, artefacts
  containing debug paths, or anything the agent spawns that communicates over a
  socket — and retaining it alongside a path-preserving mount would produce two
  disagreeing sources of truth.

- **FR-30** **macOS: scoped mounts.** A mount covers exactly one registered
  project source. Mounting a whole home directory or any other system-wide
  location is not a supported configuration. This confines FUSE latency and the
  blast radius of a failure to the project the user is actually working in.
  In the default disjoint-mount model, `exec` may rewrite source-path argv
  entries to the sanitized mountpoint, but stdout and stderr are passed through
  byte-for-byte so interactive tools retain terminal semantics. Output may
  therefore expose the internal mountpoint path.

- **FR-31** **macOS: path-preserving mode**, mounting over the source path, is
  **opt-in and off by default**, and refuses to enable unless both FR-33 (the
  retained-descriptor backing layer) and FR-32 (process identity) are in place.
  The reason it cannot be the default is data loss, not performance: macOS has no
  per-process mount view, so in path-preserving mode the user's own tools read the
  filtered view, and `git add` on a masked file stages a buffer of asterisks. One
  commit later the real secret is gone and the repository contains `****`. The
  disjoint-mountpoint model of FR-1 remains the macOS default and must stay
  supported.

- **FR-32** **Process identity.** In path-preserving mode the adapter determines,
  per operation, whether the calling process belongs to a registered agent
  session. Registered sessions receive the filtered view; every other caller
  receives unfiltered passthrough. The question is *"is this caller inside a
  registered session"*, not *"is this the registered PID"* — an agent spawns
  shells, package managers, test runners, and language servers, and all of them
  must inherit the filtered view. Mechanisms, in priority order:

  1. An inherited random session token in the child's environment, read for
     another process without cgo via `KERN_PROCARGS2` on darwin and
     `/proc/<pid>/environ` on Linux. This is primary because it survives the
     `fork`, `setsid`, and reparenting that break process-tree walking.
  2. A parent-PID walk from the caller up to a registered session root or PID 1,
     as a fallback.
  3. Process start time — `KERN_PROC_PID` on darwin, field 22 of
     `/proc/<pid>/stat` on Linux — used **only** as part of the memoization key.
     A `(pid, startTime)` pair is unique for the lifetime of a boot, which makes
     the verdict cache correct with no TTL, because a recycled PID cannot collide
     with a cached entry.

  Verdicts must be memoized keyed by `(pid, startTime)`; an unmemoized ancestry
  walk per operation is not affordable.

- **FR-33** **Retained-descriptor backing access.** The server holds a directory
  file descriptor for the source root, opened **before** the mount is
  established, and performs every backing access relative to it —
  `openat`, `fstatat`, `readlinkat`, `unlinkat`, `renameat` — with `O_NOFOLLOW`
  where a symlink must not be traversed. On Linux the handle is
  `open(src, O_PATH|O_DIRECTORY)`; macOS has no `O_PATH`, so
  `open(src, O_RDONLY|O_DIRECTORY)` serves as the `openat` base.

  This is required for two independent reasons. In path-preserving mode a
  path-resolving server would re-enter its own mount on every backing access and
  deadlock immediately. And in *any* mode, resolving a path string after the
  policy decision was made against a different resolution of that same path is a
  time-of-check-to-time-of-use window: a component swapped to a symlink between
  the two is served under the earlier decision.

- **FR-34** **Cache isolation.** A masked read is served through a handle with
  direct I/O set, so the kernel calls the server on every access and a synthetic
  redacted page can never be cached and later served to anyone. Attribute and
  entry timeouts are zero for real files, so every lookup is revalidated and a
  policy change takes effect immediately. Synthetic `.janusfs` nodes may cache
  freely.

  Per-caller kernel cache policy is **explicitly out of scope and must not be
  attempted**: the FUSE page and dentry caches are keyed by inode in the kernel,
  with no notion of which process caused the fill, so an implementation that
  appears to serve cached pages to host tools and uncached pages to agents is
  leaking. Under FR-28 the question does not arise, because host tools never
  enter FUSE.

### 3.6 Reliability and recovery

- **FR-35** **Crash recovery.** When the daemon dies without running its shutdown
  path — `SIGKILL`, an OOM kill, an escaping panic — the mount stays attached
  with no server behind it, and every process touching that path blocks or
  receives `EIO`. The kernel does not fall back to the underlying filesystem. A
  supervisor must restore the directory without user intervention.

  The supervisor is one detached process per **daemon**, not per mount, started
  by the daemon. It polls the daemon's liveness with the signal-0 existence
  probe, and on death walks the mounts registry and force-unmounts each recorded
  mountpoint **that is still mounted**, using the same unmount ladder as FR-3.

  That "still mounted" precondition is what makes the supervisor race-free
  against a clean shutdown, which has already unmounted everything and therefore
  leaves the sweep a no-op. No lease file, handshake, or coordination protocol is
  required, and none should be added.

- **FR-36** A mount attempt against a stale or broken mountpoint — one reporting
  `ENXIO`, the signature of a FUSE mount whose server is gone — force-unmounts it
  and retries once, rather than failing with an error the user must decode.

- **FR-37** A panic in any FUSE handler is recovered into `EIO` and an event; the
  mount stays up. A panic in the decision path resolves `HIDDEN`.

- **FR-38** Every mount failure names what to do next. A non-empty mountpoint
  with JanusFS mounts nested under it lists them and says which to unmount first.

### 3.7 Observability

- **FR-39** Every decision-bearing operation emits
  `{ts, op, path, decision, matchedRule, matchedPattern, bytes, latencyUs, cache}`
  where `cache` is `hit|miss|rebuild|na`. Emission is non-blocking: observability
  never blocks the data path, and a full buffer drops the event.

- **FR-40** An in-process metrics registry exposes operation counts by type and
  decision, bytes by decision, cache hit/miss/rebuild counts, redaction duration
  and handler latency histograms, active rule counts, generation and last-reload
  time, and per-path serve counts through a bounded top-N tracker. Metric handles
  for every (op, decision) pair are pre-resolved so the hot path performs no
  label lookup.

- **FR-41** One HTTP listener per daemon on `127.0.0.1:<uiPort>` (default 7381,
  `--ui-port`) serves a combined index at `/` and each mount's dashboard and API
  under `/mounts/<uuid>/`. Every `/api/v1/*` route requires a per-mount bearer
  token generated at mount time. Routes cover a live summary, coverage, per-path
  reveal, config read and write, reload, history series, sessions, and the
  virtual-file contents.

- **FR-42** Dashboard panels answer, in order: am I protected, is it being used,
  what is it costing. Coverage lists show the matched rule and pattern with level
  attribution. Panels have a time-range toggle backed by the history store, and a
  sessions view of past mounts. Every panel has a meaningful empty state; sizes,
  rates, and times are human-readable; long paths middle-truncate with the full
  path available. The bundle is at most 200 KB, entirely offline, with no
  external requests, and usable at half-screen laptop width.

- **FR-43** Degraded modes are first-class. Losing the live feed falls back to
  polling with a visible indicator; a dead server shows a clear banner and
  reconnects.

- **FR-44** Structured JSON logs to stderr via one handler configured once:
  startup config summary, generation swaps, config errors, denial events at debug
  level and rate-limited, panics. **Never file content.**

- **FR-45** A synthetic `<mountpoint>/.janusfs/` directory, listed normally in the
  root `readdir` and impossible for user rules to hide or mask, contains
  read-only `conflicts.json` (parsed rule dump, conflicts, redundancies, memory
  footprint) and `status.json` (generation, uptime, cache counters).

### 3.8 CLI and diagnostics

- **FR-46** Every CLI failure prints one line of cause and one line of remedy. No
  raw Go error or stack trace reaches the user; those go to the debug log.

- **FR-47** `janusfs mount` on success prints source, mountpoint, rule counts,
  and the dashboard URL.

- **FR-48** `janusfs check [path]` statically lints the config tree and reports
  per finding a severity, `file:line`, and a suggested fix, restricted to
  findings that indicate a real mistake: regex/glob compile errors (reported
  with their fail-closed-to-Hidden consequence), unknown builtin names,
  directory-mask rewrites (FR-10), and blocked negation attempts (FR-9, FR-17).
  It does **not** report a rule that merely matches no files in the current
  tree: a defensive pattern for files that do not exist yet is intended, and
  flagging it only trains the operator to ignore the output. Findings are
  grouped by file and sorted by severity. `--secrets` enables an opt-in,
  heuristic scan for likely secret filenames and built-in secret-pattern content
  that currently resolves Allowed; those findings are warnings only and must not
  be presented as proof of complete coverage. `--matches` adds a policy preview
  of paths that currently resolve `HIDDEN` or `MASKED`, excluding `ALLOWED` paths
  by default. Exit 1 on errors. `--json` for tooling, including matches when
  requested.

- **FR-48a** `janusfs patterns [--json]` lists every reserved built-in
  `.janusfs.yml` mask pattern name with its description and exact RE2 regex source.
  `whole-file` is shown as a sentinel with no regex. The output is for operator
  inspection and tooling, not part of the masking hot path.

- **FR-49** `janusfs explain <path>` shows the derivation of one path's decision:
  every rule considered, in order, with which matched, which was a negation, and
  which decided.

- **FR-50** `janusfs doctor [--verbose]` reports FUSE availability and version;
  active mounts **with their real mountpoints, not hashes**; per mount the
  generation, rule counts, cache entries and bytes, and history DB size; the
  supervisor's status (FR-35); and in path-preserving mode the identity-lookup
  hit rate and mean latency (FR-32). `--verbose` adds the compiled rule dump.

- **FR-51** `janusfs mounts [--json]` lists active daemon mounts plus recorded
  registry entries with status: `mounted`, `recorded`, `missing-src`, `stale`,
  or `error`. `janusfs paths` lists every config and data path with presence.
  `janusfs path <src>` prints just the mountpoint, for `cd "$(...)"`.

- **FR-52** JanusFS has a built-in mount root default of `~/.janusfs/mounts` so
  first-run mounting does not require setup. `janusfs install` optionally
  configures a custom mount root once and saves it.
  `--global-rules` also writes secure-default global rules.

- **FR-53** All commands support `--quiet` and exit codes suitable for scripting.

### 3.9 Persistence

Exactly one component persists: the history store. Compiled rules are derived and
rebuilt in milliseconds — **config stays as files, never a database**. Coverage is
the live output of the engine, queried, never stored as truth. Redacted content
is RAM only.

- **FR-54** The dashboard presents past data alongside live data: trends,
  previous sessions, historical top-N paths, and coverage snapshots.

- **FR-55** Storage is SQLite via `modernc.org/sqlite` (pure Go, no cgo). One
  database per mount at `~/.janusfs/history/<basename>-<hash12>.db`, mode `0600`,
  parent `0700`. The hash of the absolute source path is part of the filename so
  two sources with the same basename cannot share and corrupt one file.

- **FR-56** Contents are **rollups only, never raw events and never file
  content**: one-minute aggregate buckets, per-path counters for the top 1000,
  session records, and coverage snapshots at mount, at generation swaps, and at
  unmount.

- **FR-57** A dedicated goroutine consumes the event fan-out and flushes
  transactionally in batches. A slow or failed flush never blocks a FUSE handler
  or the live UI; it drops and counts. Corruption on open renames the file aside,
  starts fresh, and warns. A history failure never fails a mount.

- **FR-58** Retention is 30 days by default (`--history-retention`), pruned at
  startup and daily. `--no-history` disables persistence entirely.

  **Threat-model entry**: the history database deliberately writes path names and
  access patterns to disk. Accepted for FR-54, mitigated by rollups only, no
  content, `0600`/`0700`, retention pruning, the `--no-history` opt-out, and the
  rule that `~/.janusfs` is never a mount source.

## 4. Non-functional requirements

- **NFR-1 Security.** No unredacted byte of a `MASKED` or `HIDDEN` file may be
  observable through the mount, the HTTP API, the event feed, logs, or the virtual
  files. Events and logs carry paths and pattern names, never matched content.
  Cache memory is zeroed on eviction and shutdown, best-effort, with the Go GC
  caveat documented rather than glossed.

- **NFR-2 Fail closed under all faults.** Parser error, cache corruption,
  recovered panic, or any unexpected internal error resolves affected paths to
  `HIDDEN`, never raw. Exception, stated explicitly because it is the one place
  the direction inverts: under FR-32 an *unidentifiable caller* is treated as a
  host process and receives passthrough. A registered agent's identity is
  inherent and cannot be lost by accident in a way that matters, whereas a host
  tool is easily unidentifiable — a process that exits between the operation and
  the lookup, a `sysctl` failure under load, a process owned by another uid.
  Denying there breaks the user's editor and build for no security gain, because
  the process being denied was never the agent. See §20 for the residual risk this
  accepts.

- **NFR-3 Performance.** Measured by the built-in histograms against
  `bench/BASELINE.md`:
  - allowed-file sequential read throughput at least 85% of raw FUSE passthrough;
  - handler-added p99 latency at most 250 µs for allowed operations, 500 µs for
    masked cache hits;
  - redaction throughput at least 100 MB/s single-threaded on a 1 MB
    dotenv-shaped corpus;
  - decision resolution at most 5 µs on a cache hit and 200 µs on a
    ten-level miss. **A decision cache keyed by `(relPath, isDir, generation)` is
    required to meet the hit figure**; the generation in the key makes
    invalidation free.

- **NFR-4 Memory.** RAM cache budget 256 MB default (`--cache-max-bytes`) with
  LRU eviction. A single file over 64 MB (`--cache-max-file`) is refused from the
  cache and stream-redacted per read. Whole-file buffering for unbounded regexes
  is capped by `--redact-buffer-max` (512 MB default), beyond which the file fails
  closed.

- **NFR-5 Concurrency.** Shared state is reached through immutable-snapshot swap
  (rule sets, behind an atomic pointer) or fine-grained locks (cache entries). No
  FUSE handler blocks on a recompile, another handler's rebuild, the event bus, or
  any HTTP consumer. Cache locks are held only for bookkeeping, never across a
  rebuild or a redaction.

- **NFR-6 Reliability.** A recovered handler panic leaves the mount up. A process
  crash leaves no sensitive artefacts, the cache being RAM-only. A crash leaves no
  hung directory either — see FR-35.

- **NFR-7 Compatibility.** macOS 13+ on Apple Silicon and Intel with current
  macFUSE; Linux with FUSE available. One static binary per platform and
  architecture, universal on darwin. No cgo.

- **NFR-8 Testability.** The entire engine — rules, masking, cache, identity,
  events — is testable **without a mount**. FUSE is an adapter over internal
  interfaces. This is what keeps the suite fast and is a requirement, not a
  preference.

- **NFR-9 Linux isolation is verifiable.** FR-28's guarantee is asserted by test,
  not assumed: a test confirms the host mount table is unchanged while a child
  namespace holds a mount, and that a host process reading the source path sees
  unredacted content concurrently with an in-namespace process seeing redacted
  content at the same path.

- **NFR-10 Identity lookup cost is measured before it ships.** FR-32 adds at
  least one syscall per operation. Benchmark it against NFR-3's 250 µs budget
  *before* building on it. If it does not fit, the correct outcome is that macOS
  path-preserving mode does not ship and the disjoint model remains the macOS
  answer — an acceptable result, since Linux gets kernel-enforced isolation
  either way.

- **NFR-11 Zero host overhead on Linux.** Under FR-28, host tools must show no
  measurable regression against the no-JanusFS baseline, because they are not
  touching FUSE at all. "Reduced overhead" is not the target; zero is, and it is
  verified by benchmark rather than argued from the design.

- **NFR-12 Recovery is bounded.** After an ungraceful daemon death, FR-35's
  supervisor restores native access to every affected path within 5 seconds.

---

# Part II — Architecture

The as-built architecture, with `file:line` anchors, is documented in
[`docs/knowledge/architecture.md`](docs/knowledge/architecture.md). This part
states the design constraints that must hold.

## 5. Process and package layout

One long-lived daemon owns every mount. Mounts are structs inside it, not
processes. Short-lived CLI clients speak one JSON object per connection over
`~/.janusfs/daemon.sock`.

The exception is `janusfs exec` on Linux, which is its own FUSE server for its own
namespace (FR-28) and needs no daemon.

```
cmd/janusfs         entrypoint, manual explicit DI, cobra commands, no framework
internal/config     one Config struct, defaults, file/env overlay, validation
internal/logging    slog wrapper, one handler, per-component loggers
internal/apperrors  sentinel errors and the single ToErrno translation point
internal/rules      discovery, parse, compile, resolve; gitignore semantics
internal/engine     atomic rule-set snapshot, generations
internal/patterns   builtin and custom pattern library
internal/redact     length-preserving redaction, streaming modes
internal/provider   RedactedContentProvider: RAM cache keyed by ContentKey
internal/backing    dirfd-relative backing access (FR-33)
internal/procid     process identity and session registry (FR-32)
internal/nsexec     Linux namespace staging for exec (FR-28)
internal/mount      FUSE adapter; thin, no business logic
internal/execrunner exec orchestration
internal/obs        event bus, metrics, ring buffer, top-N
internal/history    SQLite Store: rollups, sessions, coverage
internal/health     diagnostics for doctor
internal/check      static config linter, shared by check and conflicts.json
internal/api        HTTP server; thin adapter
internal/ui         embedded dashboard assets
internal/vfsmeta    the .janusfs virtual file contents
```

There is no `internal/watch` or `internal/platform`: there is no file watcher
(FR-20), and platform differences are isolated behind build-tagged files inside
the packages that need them, not a separate package.

The dependency direction is one-way:

```
cmd/janusfs ─▶ everything
mount       ─▶ engine, provider, backing, procid, apperrors, vfsmeta
engine      ─▶ rules ─▶ patterns
provider    ─▶ redact ─▶ patterns;  provider ─▶ apperrors
procid, backing, nsexec ─▶ nothing internal
```

`provider` must not import `engine`: callers resolve a decision and pass the
compiled patterns in. This keeps the cache testable without a rule tree and
prevents it becoming a second resolution path. `procid` must not import `mount`
or `engine`, for the same reason.

## 6. FUSE adapter

Built on `hanwen/go-fuse/v2`. The adapter translates a Decision into a kernel
answer and does nothing else — it never decides and never redacts.

It embeds `fs.LoopbackRoot`/`fs.LoopbackNode` and overrides only the
operations FR-8's matrix says must differ, inheriting passthrough behaviour for
everything else. That is why `lookup` and `getattr` report real attributes for all
three decisions with no code.

The decision-to-errno translation is one gate: `gate(class, decision) → errno`,
with two op-classes — `denyNonAllowed` (mutations: create/delete/rename/chmod/
hardlink/xattr-writes require ALLOWED) and `denyHidden` (reads, traversal, and
mkdir/rmdir deny only HIDDEN). Every override consults this one gate rather than
re-deriving the check inline, so the FR-8 matrix lives in a single table (and is
tested as one, without a live mount). The two concerns stay separate:
`internal/backing` owns descriptor-relative I/O, `internal/mount` owns this
decision-to-errno gate.

The adapter reaches the real file through `internal/backing`'s dirfd-relative
layer, not a path re-join, so the decision and the I/O share one resolution.

Two distinct decision paths must be preserved:

- operations invoked **on** a node resolve that node's own path, wrapped in a
  `recover()` that folds a panic to `HIDDEN`;
- operations invoked with a **parent plus a child name** — `unlink`, `rename`,
  `symlink`, `link`, `mknod`, `mkdir`, `rmdir`, `create` — resolve the child path,
  determining directory-ness from a real stat of the child when it exists.

Masked reads re-resolve on every read (FR-22) and handle all three outcomes: still
masked serves redacted bytes, newly hidden returns `EACCES`, newly allowed reads
the real file directly.

Config-file immunity (FR-17) is checked **before** the policy lookup in every
mutating operation, so no rule can make a config file writable.

The `Adapter`, its `Mount`/`Unmount` lifecycle, and `OpEvent` are one shared
implementation (`internal/mount/mount.go`, build-tagged `darwin || linux`); the
only platform difference is `applyPlatformOptions`. On macOS that sets the
load-bearing (not cosmetic) `nobrowse` and `noappledouble` options — without
them Spotlight and Finder hold the volume busy and a graceful unmount fails with
`EBUSY` indefinitely — and `NullPermissions`, which avoids spurious `EACCES`
from ownership mismatches on the loopback; on Linux it is a no-op. `ioctl`
returns `ENOSYS` because macOS tools issue ioctls on regular files and go-fuse's
default handler panics on empty input buffers.

## 7. Decision engine

Normative interface. Change it only through a decision record, never incidentally.

```go
type Decision uint8 // Allowed | Masked | Hidden

type Resolution struct {
    Decision     Decision
    RuleRef      string   // "<file>:<line>" of the deciding rule, "" if none
    PatternNames []string
    Patterns     []*patterns.Pattern // parallel to PatternNames
    Poisoned     bool                // Hidden caused by a config error, not a rule
    Trace        []TraceEntry        // the derivation, for explain and check
    Generation   uint64
}

// Resolve never errors. Any internal inconsistency folds to Hidden.
func (e *Engine) Resolve(relPath string, isDir bool) Resolution
func (e *Engine) Generation() uint64
func (e *Engine) Reload(root string) error
```

The current rule set lives behind an atomic pointer. `Reload` compiles a full new
snapshot and swaps it, so a reader sees either the old or the new snapshot and
never blocks and never sees a half-built one.

Resolution order: ancestor ignore evaluation shallowest-first with immediate
return on hidden; the path's own ignore evaluation; directories stop here and are
never masked; then mask accumulation across applicable levels, unioning patterns
deduplicated by name and sorted so `Patterns[i]` and `PatternNames[i]` stay
parallel.

`Trace` is additive and non-normative in content but required to exist: it is what
makes `explain` able to show a derivation rather than a verdict, and what lets
`check` report a negation that FR-17's floor blocked.

Under NFR-3, `Resolve` must be memoized on `(relPath, isDir, generation)`.

## 8. Masking pipeline

Redaction is length-preserving, always. `FindSpans` merges the union of all
pattern matches; a `whole-file` sentinel short-circuits to one span. A pattern may
declare a cheap pre-filter, run before its regex, and a capture-group index, so
`env-value` masks the value and leaves `KEY=` readable.

Streaming classifies the whole pattern set once, by the most conservative mode any
single pattern requires: bounded-length patterns stream in 256 KiB chunks with a
carry-over tail so a boundary-straddling match is still seen; a line-anchored
custom regex buffers to the next newline; the whole-file sentinel, `private-key`,
and any unbounded non-line-anchored custom regex buffer the whole file under the
`--redact-buffer-max` cap.

The chunked path must redact against the entire buffered backlog and write only
the committed prefix, because a match straddling the cut point would otherwise be
missed.

Normative provider interface:

```go
type ContentKey struct {
    Path    string
    MTimeNS int64
    Size    int64
    Inode   uint64
    Gen     uint64
}

type RedactedContentProvider interface {
    ReadAt(ctx context.Context, key ContentKey, pats []*patterns.Pattern, p []byte, off int64) (int, error)
    Invalidate(path string)
    InvalidateAll()
    Stats() ProviderStats
}
```

The cache is singleflight per path: an exact key match either serves or waits on
an in-flight rebuild for that same key, so two readers never both trigger one.
Rebuilds run outside the cache lock. Pattern-set identity is a sorted join of
pattern names, which is sufficient because names are unique per builtin and per
distinct custom regex source.

Evicted and invalidated bytes are zeroed before release. An entry still being
built is never evicted, so the byte budget may transiently overshoot while builds
are in flight and catches up on the next pass.

## 9. Backing access layer

New, required by FR-33.

`internal/backing` owns a retained root directory descriptor and exposes
descriptor-relative operations to the adapter: `OpenAt`, `StatAt`, `LstatAt`,
`ReadlinkAt`, `UnlinkAt`, `RenameAt`, `MkdirAt`, `SymlinkAt`, `LinkAt`,
`FchmodAt`, and the corresponding directory-stream open.

Two invariants:

1. The descriptor is acquired **before** the mount is established, and is never
   re-derived from a path afterwards. In path-preserving mode the path no longer
   reaches the real directory at all.
2. A relative path handed to this layer is validated to contain no `..`
   component and no absolute prefix before use. `openat` with a traversing
   relative path escapes the root just as effectively as an absolute one.

This layer is where `O_NOFOLLOW` decisions live, and therefore where the TOCTOU
window is closed.

## 10. Isolation engines

Two engines, one per platform, with genuinely different guarantees. The full
analysis is in
[`docs/knowledge/platform-isolation.md`](docs/knowledge/platform-isolation.md).

| | Linux | macOS |
|---|---|---|
| Per-process mount views | `CLONE_NEWNS` | none exist |
| Who sees the mount | the agent's process tree only | every process |
| Enforcement | the kernel | a policy decision in our daemon |
| Host tool cost | zero | every access enters FUSE |
| Crash blast radius | the namespace, reaped by the kernel | the project directory hangs |
| Evadable by a determined local process | no | yes |

**Linux** (FR-28): `janusfs exec` re-execs itself with
`Cloneflags: CLONE_NEWNS|CLONE_NEWUSER` and single uid/gid mappings; the staged
process makes the mount tree recursively private, mounts the filtered view over
the source path, spawns the target command, and unmounts on exit. Inside the user
namespace the process holds the capability to mount directly, so go-fuse's direct
mount path can be used without `fusermount`.

**macOS** (FR-30, FR-31): scoped per-project mounts, disjoint by default, with
path-preserving mode opt-in and gated on FR-32 and FR-33.

## 11. Process identity

New, required by FR-32. Design detail in
[`docs/knowledge/process-identity.md`](docs/knowledge/process-identity.md).

```go
// Identity is one process, unique for this boot.
type Identity struct {
    PID       int
    StartTime int64
}

type Registry interface {
    Register(sessionToken string, root Identity)
    Unregister(sessionToken string)
    IsAgent(pid int) bool // memoized on (pid, startTime)
}
```

Platform specifics live behind one internal seam —
`startTime(pid)`, `parent(pid)`, `environ(pid)` — implemented in
`procid_darwin.go` and `procid_linux.go` with `golang.org/x/sys/unix` only.

The registry is **in memory**. It does not persist, so it cannot outlive a
reboot, so there is nothing stale to guard against.

Three mechanisms from the conventional identity-tuple design are **rejected**, and
must not be reintroduced:

- **A hashed parent-PID chain.** It answers "is this the exact chain I recorded",
  not the question being asked, and it is fragile in the *normal* case: when an
  intermediate process exits, its children reparent to `launchd` or `init`, the
  chain changes, the digest stops matching, and a legitimate agent subprocess
  silently loses its filtered view. Walk the chain; do not hash it.
- **A boot UUID.** It guards a persisted registry against a reboot. The registry
  is in memory. Dead weight.
- **Process group or session ID as the match key.** A subshell or backgrounded
  helper calling `setsid` detaches from both. They may corroborate; they cannot
  decide.

## 12. Observability internals

Two vocabularies, deliberately. The adapter emits a plain-string operation event
so `internal/mount` needs no dependency on `internal/obs`; the wiring layer
translates it into the typed `obs.Event`. `PANIC` and `CONFIG_READONLY` map to
`Hidden` plus a synthetic error, so they count as denials and stay
distinguishable.

`obs.Decision` mirrors `engine.Decision` rather than importing it, keeping the
observability layer free of the policy engine.

Emission is synchronous into pre-resolved metric handles and asynchronous into
history through a buffered channel drained by a dedicated goroutine. A full
buffer drops. Nothing in this path may ever block a FUSE handler.

## 13. HTTP API, UI, and virtual files

One listener per daemon, served by the daemon process: a combined index at `/`
and each mount's dashboard and API under `/mounts/<uuid>/` (the index and its
routing live in `cmd/janusfs/dashboard.go`). The per-mount API server is a thin
adapter holding closures injected at mount time — mount info, a decision
resolver, a reload trigger, and a stats snapshot — rather than importing the
engine or provider, so the dashboard cannot become a second resolution path.
The stats snapshot crosses that seam as a typed `api.VFSStats` value (entries,
bytes, cache hit/miss/rebuild counts), not a positional tuple.

The dashboard is served from an embedded filesystem: no external asset, no CDN,
fully offline.

The virtual `.janusfs` directory is synthesized by the adapter before any policy
lookup, which is why user rules cannot hide or mask it. `status.json` reports
generation, uptime, and cache counters; it carries no watcher field, because
there is no watcher (FR-20) — reloads are on demand.

`.janusfs` is also the mount-readiness probe used by `exec`, so it is
load-bearing beyond introspection.

## 14. Error-handling matrix

`internal/apperrors` defines every sentinel and the single translation to
`syscall.Errno`. `internal/mount` is the only permitted caller of `ToErrno`.

| Sentinel | Condition | Errno |
|---|---|---|
| `ErrSymlinkEscape` | symlink target resolves outside the source tree (FR-12) | `ENOENT` |
| `ErrRedactUnsupported` | file exceeds `--redact-buffer-max` under an unbounded regex | `EACCES` |
| `ErrRebuildTimeout` | cache rebuild exceeded its bound (FR-23) | `EIO` |
| `ErrPanic` | recovered handler panic (FR-37) | `EIO` |
| anything else | unexpected internal error | `EIO` |

The default arm is `EIO` on purpose: an unexpected error is "something went wrong,
fail closed", and must never be leaked as an errno implying a more specific and
wrong cause.

## 15. Security model

What JanusFS enforces:

- A masked or hidden file's real bytes never leave the boundary — not through
  reads, not through the API, not through logs, not through the virtual files.
- The agent cannot weaken its own policy: config files are read-only through the
  mount (FR-17), and the global level is a floor no in-tree rule can negate.
- The agent cannot escape the mask by restructuring the filesystem: creating a
  hardlink to a masked inode is denied (FR-11), renaming a masked file is denied,
  a symlink out of the tree is dangling (FR-12), and the policy matcher agrees
  with the filesystem about path identity (FR-13).
- On Linux, the boundary is the kernel (FR-28).

What it does not enforce, stated plainly:

- On macOS in path-preserving mode, the boundary is a heuristic in our daemon. A
  local process that deliberately evades identification reaches the unfiltered
  view. This is inherent to a platform with no per-process mount views, not a
  consequence of any choice made here, and it is why that mode is opt-in.
- Content protection by inode. Decisions are per path.
- Anything about a process that can read the source directory directly. JanusFS
  filters a view.

Filesystem permissions: `~/.janusfs` is `0700` throughout, files `0600`. The
history database is the only thing that writes information derived from the source
tree to disk, and it writes path names and access patterns, never content.
`~/.janusfs` must never itself be a mount source.

## 16. Configuration, logging, and process wiring

Every tunable is a field on one `config.Config`. Precedence is
`Default() → ApplyFile() → ApplyEnv() → flags`, and no package outside
`internal/config` and `cmd/janusfs` reads a flag or an environment variable.

`Validate()` runs before any FUSE call: the source exists and is a directory, and
in the disjoint model the mountpoint exists, is a directory, is empty, and does
not overlap the source. Path-preserving mode (FR-31) relaxes the overlap rule
**under that mode only**; the rule is not deleted.

Logging is one JSON handler configured once by `cmd/janusfs`, with
per-component loggers derived from it. Never `slog.Default()`.

Wiring is manual and explicit. No DI framework. `startMount` is the whole
dependency graph in one readable function, and it returns only once the mount is
actually serving, which is what lets the CLI honestly report success.

A history failure is non-fatal by design: log a warning and continue without
persistence.

## 17. History store

One SQLite database per mount, four tables: `schema_version`, `sessions`,
`op_rollups`, `coverage`. Rollups only. See FR-54 through FR-58.

---

# Part III — Delivery

## 18. Independence of requirements

Every requirement in this specification is independently implementable and
independently testable: nothing here prescribes an order of construction, a
release schedule, or a phase plan. A requirement's exit condition is simply its
own test plus the invariants in §19 — the leak oracle green, no NFR-3 budget
regressed, no new dependency added for convenience.

The only hard dependencies are the ones the requirements themselves state:
macOS path-preserving mode (FR-31) refuses to enable without the retained
descriptor layer (FR-33) and process identity (FR-32); everything else stands
alone.

## 19. Test strategy

- **Unit**, no external services, no mount. The whole engine (NFR-8). History uses
  an in-memory database.
- **Integration**, behind the `fuseintegration` build tag, mounting for real.
  Not part of the default test run.
- **The leak oracle** is a standing tripwire, not a test: sentinel secrets in
  `testdata/` must never appear in any byte read through a mount, in any test,
  ever. A failure blocks the change.
- **Benchmarks** compared against `bench/BASELINE.md`. A regression past an NFR-3
  budget blocks the change rather than warning.
- **Isolation tests** for step 4 must assert the *negative*: that the host cannot
  see the namespace mount. A test that only confirms the child sees redacted
  content would pass even if the mount had propagated to the host.

## 20. Risks and rejected designs

### Accepted risks

- **macOS path-preserving mode is evadable.** A deliberately evasive local process
  reaches the unfiltered view (§15). Accepted because the threat model's agent is
  untrusted but not purpose-built to escape, because a hostile local process could
  read the source directly anyway, and because the mode is opt-in. It must never
  be described as equivalent to the Linux guarantee.
- **`CLONE_NEWUSER` makes the child believe it is root.** Some tools behave
  differently as uid 0. Accepted as the price of unprivileged mounting; document
  it in `exec`'s help text.
- **The history database writes path names to disk** (FR-58).
- **Per-path, not per-inode decisions** (FR-11).
- **Two marked shortcuts** carry `ponytail:` comments naming their ceiling and
  upgrade path: the provider's single cache mutex, and oversize re-redaction from
  byte 0.

### Rejected designs

Recorded so they are not re-proposed.

- **A four-tier fast-path router with an "outside active scope" first tier.** A
  scoped mount has no out-of-scope operations — the kernel only sends the server
  operations for paths under the mountpoint — so the tier is unreachable code.
  What remains is the existing call sequence with one identity lookup added; that
  is not a router.
- **Global `$HOME` overmounting.** Never. Note also that mounts are already
  strictly per project source, so "shift from a global overlay to scoped project
  roots" describes work that is already done; the remaining macOS work is parity,
  not scoping.
- **Per-caller kernel cache policy** (FR-34). Not implementable; an
  implementation that appears to work is leaking.
- **A hashed parent-PID chain, a boot UUID, and process-group matching** (§11).
- **`EACCES` on an identity-lookup failure** (NFR-2). Fails the wrong thing
  closed.
- **Passing a `/dev/fuse` descriptor from the daemon into the child namespace
  over `SCM_RIGHTS`.** go-fuse exposes no supported API for adopting an
  externally created FUSE descriptor, so this would mean hand-rolling `mount(2)`
  with FUSE option strings plus descriptor passing — a large amount of unsafe
  plumbing for no user-visible gain over having the `exec` process serve its own
  mount.
- **A separate `janus-watchdog` binary** (FR-35). It would duplicate the unmount
  ladder and the registry loader for nothing. A hidden subcommand of the same
  binary reuses both.
- **Improving the path rewriter.** The failure is structural, not a matter of
  quality (FR-29).

---

# Part IV — Instructions for the implementing agent

## 21. Ground rules

- **Fail closed is the tiebreak.** Any ambiguity resolves to the option where the
  agent behind the mount sees less. The one documented inversion is NFR-2's
  treatment of unidentifiable callers, and its reasoning is stated there.
- **Every behaviour traces to a requirement.** If you need something this document
  does not define, implement the fail-closed reading, write a decision record, and
  say so. Do not invent silently.
- **The interfaces in §7, §8, §9, and §11 are normative.** Change them through a
  decision record, never incidentally while implementing something else.
- **No cgo.** This is why history uses `modernc.org/sqlite` and why process
  inspection must go through `golang.org/x/sys/unix`.
- **Dependency policy**: `hanwen/go-fuse/v2`, `modernc.org/sqlite`,
  `prometheus/client_golang`, `spf13/cobra`, `jmoiron/sqlx`, `google/uuid`,
  `golang.org/x/sys`, and stdlib are allowed. Anything else needs a decision
  record first. Never network-calling libraries or telemetry SDKs.
- **Never log file content.** Not a byte slice from `redact`, not one from
  `provider`, not at debug level. Review-blocking.
- **Read [`docs/knowledge/`](docs/knowledge/index.md) before reading code.** It
  exists so you do not have to re-derive the architecture, and it carries the
  `file:line` anchors. If you find it wrong, fix it in the same change and add a
  line to its `log.md`.

## 22. Repository conventions

- Packages are lowercase single words; files are `snake_case.go`. Platform
  variants go in separate files behind build tags.
- **Never cite this document from a code comment.** No `FR-`/`NFR-` numbers, no
  `SPEC.md §x`, no amendment dates. A comment states the constraint or the reason
  the code cannot show for itself; a document coordinate is provenance, and
  provenance rots. If a comment currently reads "implements FR-7's matrix",
  rewrite it to say what the behaviour is and why. Requirements are traced in the
  other direction: this document and
  [`docs/knowledge/`](docs/knowledge/index.md) point at code, not the reverse.
- No errno construction outside `apperrors.ToErrno`, called only from
  `internal/mount`.
- No SQL outside `internal/history`'s `Store` methods.
- No flag or environment reads outside `internal/config` and `cmd/janusfs`.
- No logging outside `logging.New(component)`.
- `internal/mount` and `internal/api` translate and delegate. They do not decide,
  redact, or query.
- Wrap errors with `%w` at the call site and let them propagate. The CLI prints
  exactly one line. User-facing messages are written for an operator: no package
  prefix, no wrapped syscall text, always a cause and a remedy.
- A deliberate shortcut with a known ceiling gets a `ponytail:` comment naming the
  ceiling and the upgrade path.
- Conventional Commit prefixes are required, not preferred: they drive the release
  changelog grouping.

## 23. Definition of done

A change is not done until all of these hold.

1. `make verify` passes — `fmt-check`, `vet`, `test-race`.
2. Every new branch, parser, loop, or security-relevant path has one runnable
   check that fails if the logic breaks.
3. The leak oracle is green.
4. No benchmark regressed past an NFR-3 budget.
5. Anything user-visible has a cause-and-remedy message, and the help text says
   what changed.
6. Anything that changes architecture updates
   [`docs/knowledge/`](docs/knowledge/index.md) and appends to its `log.md`.
7. Anything that closes an item in
   [`docs/knowledge/known-gaps.md`](docs/knowledge/known-gaps.md) removes it from
   that register rather than leaving it to rot.

## 24. Working style

- Prefer the smallest change that is correct in the right place. A small change in
  the wrong place is a second bug.
- Fix root causes. Before changing a function, check its other callers; a guard in
  the shared function is a smaller change than a guard in each caller, and it
  leaves no sibling broken.
- Delete rather than add where you can. FR-29 deletes code, and that is the point.
- Stop and ask only when a decision changes the security model, the normative
  interfaces, or the dependency policy. Everything else: pick the fail-closed
  reading, record it, continue.
