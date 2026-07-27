---
type: Subsystem
title: FUSE adapter
description: The as-built operation matrix over go-fuse's loopback, config-file immunity, and the synthetic .janusfs directory.
tags: [fuse, mount, go-fuse, mutation]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: node
    resource: /internal/mount/janus_node.go
    title: JanusRoot, JanusNode, maskedHandle
  - id: virtual
    resource: /internal/mount/janus_virtual.go
    title: janusVirtualDir, janusVirtualFile
  - id: darwin
    resource: /internal/mount/mount_darwin.go
    title: Adapter.Mount, mount options (darwin)
  - id: linux
    resource: /internal/mount/mount_linux.go
    title: Adapter.Mount (linux)
---

# Strategy: embed the loopback, override only what differs

`JanusRoot` embeds `fs.LoopbackRoot` and `JanusNode` embeds `fs.LoopbackNode`
(`internal/mount/janus_node.go:35` and `:47`). `newJanusRoot` (`:57`) mirrors
`fs.NewLoopbackRoot`'s construction but replaces the `NewNode` hook so children
are `JanusNode`s.

Everything not overridden is inherited passthrough behaviour. That is why
`lookup` and `getattr` report real attributes for all three decisions with no
code: the real file is always stat'd.

`isDir` is captured at construction from the real file's `stat` because
`LoopbackNode`'s own directory bookkeeping is not exported (`:70`).

# Descriptor-relative backing access

The read path does not resolve a backing-file path string. `newJanusRoot`
opens the source directory once (via `internal/backing.Open`) and stashes the
retained descriptor on `JanusRoot.Backing`. `contentKey` uses `Backing.StatAt`,
`decisionFor` uses `Backing.LstatAt`, `readRaw` opens with
`Backing.OpenAt(rel, O_RDONLY|O_NOFOLLOW, 0)`, and the provider's rebuild
opener (`maskedHandle.backingOpener`) opens through the same descriptor.
`ContentKey.Path` is retained as a plain string, but only as the cache map's
identity key — the actual bytes always come through the descriptor. This
closes the window a path re-open would leave between the decision and the
I/O: the decision and the read now traverse the same, retained resolution.

Mutations still go through the embedded `LoopbackNode` (path-based). The
split is deliberate — the read path is the security-critical one, and the
mutation/dir-stream/file-handle plumbing that `LoopbackNode` provides is a
separate, larger conversion.

# Operation matrix, as built

Every row below is a real override in `janus_node.go`. "passthrough" means
delegation to the embedded `LoopbackNode`.

| Operation | ALLOWED | MASKED | HIDDEN | Anchor |
|---|---|---|---|---|
| `lookup`, `getattr` | real attrs | real attrs | real attrs | inherited; `Getattr` at `:207` only zeroes `Ino` |
| `open` read-only | passthrough fd | `maskedHandle` + `FOPEN_DIRECT_IO` | `EACCES` | `:217` |
| `open` write-intent | passthrough | `EACCES` | `EACCES` | `:230` |
| `read` | passthrough | redacted bytes | n/a (open denied) | `:527` |
| `opendir` | passthrough | n/a | `EACCES` | `:254` |
| `readdir` | passthrough, plus injected `.janusfs` at root | — | — | `:170` |
| `setattr` (chmod/chown/utimens/truncate) | passthrough | `EACCES` | `EACCES` | `:269` |
| `unlink` | passthrough | `EACCES` | `EACCES` | `:286` |
| `mkdir`, `rmdir` | passthrough | n/a | `EACCES` | `:304`, `:317` |
| `rename` | passthrough only if **both** source and destination are Allowed | `EACCES` | `EACCES` | `:332` |
| `symlink`, `mknod`, `create` | passthrough only if the new name is Allowed | `EACCES` | `EACCES` | `:365`, `:391`, `:406` |
| `link` | passthrough only if **both** the target inode's own path and the new name are Allowed | `EACCES` | `EACCES` | `:378` |
| `readlink` | passthrough, `ENOENT` if the target escapes the root | same | `EACCES` | `:428` |
| `getxattr`, `listxattr` | passthrough | passthrough | `EACCES` | `:460`, `:474` |
| `setxattr`, `removexattr` | passthrough | `EACCES` | `EACCES` | `:487`, `:500` |
| `ioctl` | `ENOSYS` always | `ENOSYS` | `ENOSYS` | `:245` |

Three structural notes:

**`Link` checks the target inode's own decision, not just the new name's.**
An earlier version of this method checked only `decisionFor(name)` — the new
link name — which let an agent launder a masked file in one syscall:
`link("secrets.env", "copy.txt")` then `cat copy.txt` for plaintext, since
`copy.txt`'s own decision was `Allowed` even though `secrets.env` was masked.
The fix resolves `target`'s decision (via a type assertion to `*JanusNode`) and
denies unless both it and the new name are `Allowed`; a `target` that isn't a
`*JanusNode` is denied outright. This only governs a *new* hardlink created
through the mount — a hardlink already present on disk before the mount existed
may still carry different decisions per path, which is an accepted, documented
property (protecting content by inode is a non-goal).

**There is no `Write` override, and none is needed.** A write can only reach the
backing file through a file handle, and a write-intent `Open` on a masked or
hidden path already returns `EACCES` (`:230`). `Create` and `Setattr` cover the
other two ways to mutate. The absence is correct by construction rather than an
oversight — but see the reload-window caveat in
[known gaps](known-gaps.md).

**`Ioctl` returns `ENOSYS` deliberately.** macOS tools such as `make` issue
ioctls on regular files, and go-fuse's default `LoopbackFile.Ioctl` panics on
empty input buffers. `ENOSYS` is the correct answer for a filesystem that does
not support ioctls (`:241`).

# Two ways a decision is obtained

- `resolve()` (`:122`) for operations invoked *on* the target node. Wrapped in
  a `recover()` that folds a panic to `Hidden` with `Poisoned: true`, emitting a
  `PANIC` observation.
- `decisionFor(name)` (`:140`) for operations invoked with a parent inode plus a
  child name — `Unlink`, `Rename`, `Symlink`, `Link`, `Mknod`, `Mkdir`,
  `Rmdir`, `Create`. It does an `os.Lstat` of the real child path to learn
  `isDir`, defaulting to `false` for a name about to be created.

`decisionFor` therefore costs one `Lstat` plus one full `Resolve` per mutation
operation.

# Config-file immunity

`isConfigFile(relPath)` (`:82`) matches any basename equal to `.janusignore` or
`.janusmask`. Those files are unconditionally read-only through the mount,
checked *before* the policy lookup, in `Open` (`:219`), `Setattr` (`:271`),
`Unlink` (`:289`), `Rename` as both source and destination (`:336`, `:342`),
and `Create` (`:409`). The observation decision string is
`CONFIG_READONLY`.

This is the property that stops an agent from weakening its own sandbox by
editing the policy that governs it. Combined with the global-tier floor
described in [policy engine](policy-engine.md), it is the cross-trust-boundary
guarantee.

# Symlink escape

`Readlink` (`:428`) reads the real link target and then calls `escapesRoot`
(`:448`), which resolves a relative target against the link's own directory,
cleans it, and checks whether it still lies within the mount root. A target
outside the root is served as `ENOENT` — dangling — so the mount cannot become
an escape hatch to an unprotected path.

Note the UX consequence: a legitimately symlinked dependency directory inside a
repo that points outside the tree becomes unreadable through the mount. That is
fail-closed, and correct by the current threat model, but it is a real
compatibility cost.

# Masked reads re-resolve every time

`maskedHandle.Read` (`:527`) does not trust the decision captured at open time.
It re-resolves on every call, and handles all three outcomes:

- still `Masked` → redacted bytes via the provider;
- now `Hidden` (a reload tightened policy) → `EACCES`;
- now `Allowed` (a reload loosened policy) → `readRaw` (`:578`), a direct read
  of the real file bypassing the redaction pipeline.

Any panic in the provider or redaction path is recovered into `EIO` via
`apperrors.ToErrno` rather than crashing the mount (`:529`).

# The synthetic `.janusfs` directory

`Lookup` (`:152`) and `Readdir` (`:170`) synthesize a `.janusfs` directory at
the mount root only. It does not exist on disk and user rules cannot hide or
mask it, because it is intercepted before any policy lookup.

`internal/mount/janus_virtual.go` implements it: mode `0555`, containing
`conflicts.json` (from `vfsmeta.ConflictsJSON`) and `status.json` (generation,
uptime, provider stats), both mode `0444`, both `FOPEN_DIRECT_IO`, write-intent
opens denied (`janus_virtual.go:106`).

`status.json` currently reports `watcherAlive: false` unconditionally, since
reload is on demand (`janus_virtual.go:88`).

`janusfs exec` uses the existence of `<mountpoint>/.janusfs` as its mount
readiness probe (`internal/execrunner/runner.go:150`), so this directory is
load-bearing beyond introspection.

# Mount options

Both platforms set `FsName`/`Name` to `janusfs` so `df` shows something
meaningful, and wire `fs.Options.Logger` to a component logger.

Darwin additionally sets (`mount_darwin.go:97`):

- `NullPermissions` — let the kernel check permissions against reported mode
  bits instead of having go-fuse do it, avoiding spurious `EACCES` on
  ownership mismatches;
- `nobrowse` — keep the volume out of Finder and Spotlight;
- `noappledouble` — stop the `._*` and `.DS_Store` writes Finder would make.

The last two are not cosmetic. macFUSE holds a volume busy by default once
`mdworker` indexes it and Finder browses it, and a graceful unmount then fails
with `EBUSY` indefinitely. These options are the standard cure.

Linux sets none of those (`mount_linux.go:77`) and does not currently enable
`DirectMount`.

# Attribute and entry timeouts

Both platforms explicitly set `fs.Options.AttrTimeout`, `EntryTimeout`, and
`NegativeTimeout` to zero (`mount_darwin.go`, `mount_linux.go`) — a deliberate
choice, not the accident of omission it once was. Regular `JanusNode` responses
still do not call `SetAttrTimeout`/`SetEntryTimeout` individually; the
mount-wide zero default covers them. Only the synthetic `.janusfs` nodes set a
longer timeout, one hour (`janus_node.go:160`, `janus_virtual.go:59`), since
their content is generated, not policy-governed.

The effect is that real files get no kernel dentry or attribute caching and are
revalidated on every lookup: a policy reload takes effect on the very next
lookup of an affected path, with no remount, because the kernel is never
allowed to answer from a cached pre-reload dentry or attribute. This costs an
upcall per lookup, which is the intended trade rather than a bug — and it is
also why memoizing `engine.Engine.Resolve` (see
[policy engine](policy-engine.md)) mattered: it buys back, at the decision
layer, most of the latency this deliberate zero-caching gives up at the kernel
layer. Verified behaviourally by
`TestReloadTakesEffectWithoutRemount` (`internal/mount/integration_test.go`):
tighten a file from Allowed to Hidden via `.janusignore` plus a live
`Engine.Reload`, with no remount, and the very next read fails closed.
