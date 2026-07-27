# PRP 05 — Descriptor-relative backing layer

**Size** L · **Blocked by** nothing · **Touches** new `internal/backing`,
`internal/mount`

## Goal

Replace path-based backing access with access relative to a directory file
descriptor held for the source root, acquired before the mount exists. This closes
a time-of-check-to-time-of-use window, and it is the hard prerequisite for
mounting over the source path on macOS.

## Why

Two independent reasons.

**TOCTOU.** Every backing access resolves a path string: `absPath()` joins root
and relative path (`internal/mount/janus_node.go:114`), `decisionFor` does an
`os.Lstat` on a joined path (`:143`), `contentKey` a `syscall.Stat` (`:599`),
`readRaw` an `os.Open` (`:579`), and `LoopbackNode` resolves the same way
internally. The policy decision is made against one resolution of the path; the
I/O happens against another. Swap a component for a symlink in between and the
read is served under the earlier decision.

**Path-preserving mode is impossible without it.** Once the view is mounted over
`src`, a server that resolves `src/foo` re-enters its own mount and deadlocks
immediately. A retained descriptor is the only way the server can still reach the
real directory.

This is also where the feature request's "canonical path evaluation" belongs. Note
that the request's framing — `filepath.EvalSymlinks` before glob matching — is not
quite the right mechanism: symlink *escape* is already handled
(`janus_node.go:428` returns `ENOENT` for a target outside the root), and calling
`EvalSymlinks` on every operation would both cost a full path walk per op and
break legitimately symlinked dependency directories. The real gap is that the
decision and the I/O can disagree about which file a path names, and a retained
descriptor with `O_NOFOLLOW` closes that directly.

## Context

- The gap, with anchors: [`known-gaps.md`](../docs/knowledge/known-gaps.md) item 5
- Why path-preserving needs it:
  [`platform-isolation.md`](../docs/knowledge/platform-isolation.md), "The
  overmount recursion trap"
- Current adapter structure:
  [`fuse-adapter.md`](../docs/knowledge/fuse-adapter.md)
- Requirements: [SPEC.md FR-33](../SPEC.md#35-isolation-and-path-parity),
  [§9](../SPEC.md#9-backing-access-layer)

## The cost, stated up front

`JanusNode` embeds `fs.LoopbackNode` and inherits most of its behaviour
(`janus_node.go:47`). `LoopbackNode` is path-based. So this task means **giving up
that inheritance** for anything that touches the backing filesystem and writing
those operations by hand.

That is a real cost and it is why this is sequenced after the cheap wins. Do not
attempt it as a refactor-in-passing inside another PRP.

## Blueprint

### Task 1 — `internal/backing`

A package with no dependency on `internal/mount` or `internal/engine`, so it is
testable on its own against a temp directory.

```go
// Root is a handle to a directory that stays valid regardless of what is later
// mounted over its path. Every operation is relative to the retained descriptor,
// so nothing here re-resolves the root's path after construction.
type Root struct {
	fd   int
	path string // for diagnostics and error messages only, never for access
}

// Open acquires the descriptor. Must be called BEFORE any mount is established
// over dir, since afterwards dir no longer names the real directory.
func Open(dir string) (*Root, error)
func (r *Root) Close() error

func (r *Root) OpenAt(rel string, flags int, mode uint32) (int, error)
func (r *Root) StatAt(rel string) (unix.Stat_t, error)   // follows symlinks
func (r *Root) LstatAt(rel string) (unix.Stat_t, error)  // does not
func (r *Root) ReadlinkAt(rel string) (string, error)
func (r *Root) UnlinkAt(rel string, dir bool) error
func (r *Root) RenameAt(oldRel, newRel string) error
func (r *Root) MkdirAt(rel string, mode uint32) error
func (r *Root) SymlinkAt(target, rel string) error
func (r *Root) LinkAt(oldRel, newRel string) error
func (r *Root) ChmodAt(rel string, mode uint32) error
func (r *Root) OpenDirAt(rel string) (*Dir, error)
```

Platform split for the descriptor flags only:

```go
// backing_linux.go:  unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
// backing_darwin.go: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
//   macOS has no O_PATH; O_RDONLY|O_DIRECTORY serves equally well as an openat
//   base, it just also permits reading the directory itself.
```

### Task 2 — Validate every relative path

This is the security-critical part of the package, and it is small enough that
there is no excuse for getting it wrong.

```go
// validRel reports whether rel is safe to pass to an *at syscall relative to a
// root descriptor. openat with a traversing relative path escapes the root just
// as effectively as an absolute one, so this is a boundary check, not a
// convenience.
func validRel(rel string) error
```

Reject: an absolute path, any `..` component, any empty component, and `.` other
than as the whole path. Compare **components after splitting on `/`**, not
substrings — a substring check for `".."` both misses `a/../b` framings you did
not anticipate and falsely rejects a legitimate file named `..foo`.

Every exported method calls this first. No exceptions, including the ones that
"obviously" receive clean input from the adapter, because the adapter's input
ultimately comes from the untrusted side.

**Test this hard.** A table test is the right shape:

```go
{"..", false}, {"a/../b", false}, {"/abs", false}, {"a//b", false},
{"", false}, {".", true}, {"a", true}, {"a/b", true}, {"..foo", true},
{"a/..foo/b", true}, {"foo..", true},
```

### Task 3 — Use `O_NOFOLLOW` deliberately

This layer is where symlink-following decisions live, so make each one explicit
rather than inheriting a default.

- Opening a **regular file** for a passthrough read: `O_NOFOLLOW`. The decision was
  made for this path; if the final component is now a symlink, the file the
  decision was about is not the file being opened.
- `ReadlinkAt`: necessarily does not follow.
- `LstatAt` for `decisionFor`'s directory-ness check: does not follow, matching
  the current `os.Lstat` semantics.
- Following a symlink to a path **inside** the tree stays supported, because the
  kernel resolves it component by component through the mount and each component
  gets its own policy decision. `O_NOFOLLOW` at this layer does not break that; it
  only stops the *server* from following a link the *kernel* did not.

Record each choice in a one-line comment at its call site. A future reader will
otherwise assume `O_NOFOLLOW` was reflexive.

### Task 4 — Rewire the adapter

Add `Backing *backing.Root` to `JanusRoot` (`janus_node.go:35`), acquired in
`newJanusRoot` (`:57`) before `fs.Mount` is called.

Then replace, one at a time, each site that resolves a backing path:

| Site | Now | Becomes |
|---|---|---|
| `absPath()` `:114` | `filepath.Join(root, rel)` | delete; callers take `rel` |
| `decisionFor` `:143` | `os.Lstat(join(...))` | `root.Backing.LstatAt(rel)` |
| `contentKey` `:599` | `syscall.Stat(absPath())` | `root.Backing.StatAt(rel)` |
| `readRaw` `:579` | `os.Open(absPath())` | `root.Backing.OpenAt(rel, O_RDONLY|O_NOFOLLOW, 0)` |
| `provider` cache key `Path` | absolute path string | keep the string as the cache key, but read via the descriptor |
| every inherited `LoopbackNode` op that touches disk | inherited | hand-written over `Backing` |

Keep `ContentKey.Path` a string. It is only an identity for the cache map, not an
access path, and changing it would churn the cache API for nothing. Add a comment
saying so, because the next reader will assume it is used for I/O.

**Sequence this incrementally.** Convert one operation, run the full suite plus
the leak oracle, commit. Do not convert all of them and then debug. The suite is
fast and the failure modes here are subtle.

### Task 5 — Prove the window is closed

A regression test that fails before this PRP and passes after, in
`internal/mount/`, tagged `fuseintegration`:

```
1. source tree with regular file  data.txt  (ALLOWED) and  secret.env (MASKED)
2. resolve a decision for data.txt          (check)
3. between check and use, replace data.txt with a symlink → secret.env
4. complete the read
5. assert the read does NOT return secret.env's plaintext
```

Racing check against use deterministically is awkward. Use a test-only hook — an
injectable function on `JanusRoot`, nil in production — invoked between decision
and I/O, so the swap happens at exactly the right instant. A hook that is nil
outside tests is acceptable; a `time.Sleep` race is not, because it will be flaky
and someone will delete it.

## Validation

```bash
rtk make verify
rtk make leak-oracle          # after EVERY converted operation, not just at the end
rtk make integration          # real mount, real syscalls
rtk make bench                # openat vs path resolution: expect neutral-to-faster
```

The leak oracle is the primary safety net for this PRP. Every conversion changes
how bytes are fetched, and a mistake looks exactly like a leak.

## Done when

- [ ] `internal/backing` exists with no `mount`/`engine` dependency and its own
      tests against a temp directory
- [ ] `validRel` has a table test covering `..` in every position, absolute paths,
      empty components, and the `..foo` false-positive case
- [ ] No site in `internal/mount` joins the root path to reach a backing file
- [ ] The TOCTOU regression test fails on `main` and passes here
- [ ] Every `O_NOFOLLOW` decision has a one-line reason at its call site
- [ ] Leak oracle and integration suite green
- [ ] No benchmark regression
- [ ] Item 5 removed from [`known-gaps.md`](../docs/knowledge/known-gaps.md);
      [`fuse-adapter.md`](../docs/knowledge/fuse-adapter.md) updated to describe
      descriptor-relative access; a line appended to
      [`log.md`](../docs/knowledge/log.md)

## If this is wrong

- Assumes the inherited `LoopbackNode` operations can be replaced without
  reimplementing go-fuse's directory-stream and file-handle plumbing. If
  `OpendirHandle` or the read/write handle path turns out to need substantially
  more of `LoopbackFile` than expected, **stop and report with a scope estimate**
  rather than growing this PRP silently. Splitting into "reads" and "mutations" is
  a legitimate outcome.
- Assumes macOS `O_RDONLY|O_DIRECTORY` works as an `openat` base for every
  operation here. If some `*at` call rejects it, report the specific syscall.

## Anti-patterns

- Do not call `filepath.EvalSymlinks` per operation to satisfy "canonical path
  evaluation". It is a full path walk per op, and it breaks symlinked dependency
  directories that legitimately point outside the tree.
- Do not skip `validRel` on paths that come from the adapter. The adapter's input
  comes from the untrusted side.
- Do not substring-match for `".."`.
- Do not convert every operation in one commit.
- Do not change `ContentKey`'s shape. It is a cache identity, not an access path.
- Do not use a sleep to produce the TOCTOU race.
