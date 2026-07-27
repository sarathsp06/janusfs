# PRP 08 — Reload revocation of open handles

**Size** S · **Blocked by** [03](03-decision-cache.md) · **Touches**
`internal/mount` · **Status** implemented (this branch)

## Goal

Close the window between a policy reload and the release of an
already-open passthrough handle: a file opened while `Allowed` must stop
serving raw bytes as soon as a reload tightens it to `Masked` or `Hidden`.

## Why

`maskedHandle.Read` re-resolves the decision per read, so a file that was
masked at open time correctly picks up a tightening reload on its next
read. A **passthrough** handle is not that — it is go-fuse's
`LoopbackFile` with a raw fd and no interception. Reads through it after a
reload continue to hit the real file until the fd is closed.

The window is small — it requires a reload to happen while an agent
already has the file open — but "tighten the policy" is exactly when the
user expects the tightening to apply. Once PRP 03 memoized `Resolve`, the
cost of a per-read re-check dropped to a cache-hit resolve (~55 ns on
this machine, well inside NFR-3), which is what makes closing this
window essentially free.

## Design

Wrap the passthrough handle. On every Read and Write, call `resolve()` on
the owning node; if the decision is no longer `Allowed`, fail closed with
`EACCES`. Every other file operation (Release, Flush, Fsync, Getlk/Setlk/
Setlkw, Lseek, Setattr, Getattr, PassthroughFd) forwards to the
underlying `*fs.LoopbackFile` unchanged, so no fd is leaked and no
attribute path diverges from LoopbackFile's semantics.

The forwarding is done by embedding the concrete `*fs.LoopbackFile` into
the wrapper — a Go method-promotion trick that gives every LoopbackFile
method to the wrapper for free. Embedding the `fs.FileHandle` interface
(which is `interface{}`) would NOT work, because its method set is
empty and nothing would be promoted; embed the concrete type.

The go-fuse notification path (an alternative fix — invalidate affected
inodes on reload so the kernel forces a reopen) is not taken here. It is
strictly stronger (the kernel sees the revocation, not just each Read),
but relies on notification correctness across two OSes and depends on
which inode entries the reload changed. The per-read re-check is simpler,
correct-by-construction (the read cannot see a stale decision because it
re-fetches the decision), and its cost is negligible given the decision
cache. If a future workload proves the read-time re-check dominates
latency, the notification path is a drop-in replacement.

## Anti-patterns

- **Do not embed `fs.FileHandle`.** It is `interface{}`; nothing is
  promoted; every non-overridden call misses; the fd leaks.
- **Do not skip the re-check on Write.** A write to an fd opened
  `Allowed` and now `Masked` writes plaintext to a file the caller is no
  longer supposed to see contents of. Cheap to prevent, same shape as
  Read.
- **Do not rely on the go-fuse notification path unless you also test
  it.** It is more powerful but has its own correctness surface; the
  per-read re-check does not.

## Done when

- [x] `revocableHandle` wraps every `Allowed` `Open` result; `Read` and
      `Write` re-check the decision.
- [x] Every other LoopbackFile method forwards unchanged via
      method promotion.
- [x] `fuseintegration` regression test opens a file while `Allowed`,
      tightens policy to `Hidden`, and asserts the next read on the same
      fd returns `EACCES`.
- [x] Item removed from `docs/knowledge/known-gaps.md`; entry added to
      `log.md`.
