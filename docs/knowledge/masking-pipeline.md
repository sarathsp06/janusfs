---
type: Subsystem
title: Masking pipeline
description: Length-preserving redaction, the streaming modes, and the RAM cache whose key is the authoritative change detector.
tags: [redact, provider, cache, secrets]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: redact
    resource: /internal/redact/redact.go
    title: FindSpans, Redact, Stream
  - id: provider
    resource: /internal/provider/provider.go
    title: RamCache, ContentKey, rebuild
  - id: node
    resource: /internal/mount/janus_node.go
    title: maskedHandle.Read, contentKey
---

# The invariant

Redaction is **byte-length preserving**. Every matched span is overwritten with
`'*'` (0x2A), one byte per original byte (`internal/redact/redact.go:99`). File
size never changes, so `stat` results stay truthful and tools that seek or
mmap are not confused. This is why the FUSE adapter can return real attributes
for a masked file while serving synthetic bytes.

# Finding spans

`FindSpans(buf, base, pats)` (`redact.go:44`) returns absolute byte ranges to
mask, merged into their union.

- A `WholeFile` pattern short-circuits to a single span covering everything, no
  matching needed.
- Each pattern may carry a `PreFilter` — a cheap substring test run before the
  regex, skipping the pattern entirely when it cannot match (`redact.go:53`).
- If a pattern declares `GroupIndex > 0`, the masked span is that capture group
  rather than the whole match (`redact.go:58`). This is what lets `env-value`
  mask the value and leave `KEY=` readable, and `db-uri` mask only
  `user:pass`.
- `mergeSpans` (`redact.go:74`) sorts by offset and coalesces overlapping or
  adjacent ranges, so a byte masked by any pattern stays masked.

`Redact` returns `buf` itself unmodified when there are no spans
(`redact.go:102`) — a deliberate clone bypass — and otherwise
`bytes.Clone`s before overwriting.

# Streaming modes

`Stream(w, r, pats, maxBufferBytes)` (`redact.go:188`) exists because a pattern
match can straddle a read boundary. `classify` (`redact.go:153`) picks the most
conservative mode any single pattern in the set requires:

| Mode | When | Behaviour |
|---|---|---|
| `modeChunked` | every pattern has a bounded match length | 256 KiB chunks, carry a `carryLen` tail forward so a boundary-straddling match is still seen |
| `modeLine` | a custom regex anchored with `(?m)` | buffer to the next newline, flush complete lines |
| `modeWholeFile` | the `whole-file` sentinel, `private-key`, or any unbounded non-line-anchored custom regex | buffer everything, capped by `maxBufferBytes` |

`builtinCarryLen` (`redact.go:139`) gives each bounded builtin a generous
carry length (4096 for `env-value`, 8192 for `jwt`, 128 for `aws-key`, …),
defaulting to 4096. `private-key` is unbounded because a PEM block has no
maximum length, so it forces whole-file mode.

`flushExceptTail` (`redact.go:290`) has a subtlety worth preserving: it redacts
against the *entire* buffered backlog, not just the committed prefix, because a
match straddling the cut point would otherwise be missed. Only the prefix is
written; the carried tail keeps its original bytes and is rescanned with the
next chunk.

Exceeding `maxBufferBytes` returns `ErrBufferExceeded` (`redact.go:202`). This
function only enforces the cap; mapping it to a fail-closed `HIDDEN` is the
caller's job, and `provider` does that by wrapping it as
`apperrors.ErrRedactUnsupported` → `EACCES`.

# The cache and its key

`provider.RamCache` is a single-mutex LRU of redacted bytes
(`internal/provider/provider.go:96`). The lock only ever guards map and list
bookkeeping — never a rebuild, never a `redact` call — which is what satisfies
"no FUSE handler blocks on another's rebuild".

`ContentKey` is the whole correctness story: the absolute real path (identity
/ map key only, never an access path), mtime in nanoseconds, size, inode, and
the rule-set generation. Its fields are **unexported**; the only way to build
one is `provider.NewContentKey(path, mtimeNS, size, inode, gen)`. That keeps the
freshness contract — which fields define staleness — in `internal/provider`,
right next to the whole-struct equality check that is the authoritative change
detector, so a caller in another package cannot silently omit a field (e.g. the
generation) and defeat staleness detection. A `Path()` accessor exposes the
identity for callers that need it.

`maskedHandle.contentKey()` (`internal/mount/janus_node.go`) builds the key with
a fresh stat of the real file (through the retained backing descriptor) on
**every read**, never caching it for the lifetime of the handle. That is
deliberate: it means a concurrent edit to the real file is always caught, and it
is the reason the project can ship without a file watcher at all. The key is
authoritative; nothing else detects change.

Including `Gen` means a rule reload invalidates every cached redaction
implicitly, on top of the explicit `InvalidateAll()`.

# Read flow

`ReadAt` (`provider.go:123`):

1. `key.Size > maxFile` bypasses the cache entirely and streams
   (`readOversize`).
2. An exact key match serves the cached entry, or waits on an in-flight rebuild
   for that same key — singleflight per path, so two readers never both trigger
   a rebuild.
3. A stale or absent entry starts a rebuild goroutine. If the previous entry
   was built and its **pattern signature is unchanged**, the stale bytes are
   handed to this caller immediately while the rebuild runs for the next one
   (`provider.go:165`). Redacted bytes are only ever superseded by other
   redacted bytes, so this is safe; raw bytes are never served for a masked
   path under any interleaving.
4. Otherwise the caller waits on the rebuild, bounded by `rebuildTimeout`
   (10 s, `provider.go:37`), then fails with `apperrors.ErrRebuildTimeout` →
   `EIO`.

`patternSignature` (`provider.go:325`) is a sorted, NUL-joined list of pattern
names. Names are unique per builtin and per distinct custom regex source, so
this is a sufficient identity for "the pattern set is unchanged".

`waitAndServe` has a guard for the generation race: if the entry that just
became ready belongs to a different pattern signature than the caller needs, it
fails closed rather than serving mismatched content (`provider.go:198`).

# Oversize files

`readOversize` (`provider.go:224`) re-redacts from byte 0 through
`off+len(p)` on every read, because a match could begin before the requested
offset. Cost grows with offset. This is a marked shortcut — see the
`ponytail:` comment at `provider.go:220` — accepted because oversize masked
files are rare, with a tmpfs shadow provider named as the upgrade path.

# Zeroization

Evicted and invalidated entries have their bytes zeroed before release
(`zero`, `provider.go:345`, called from `invalidateLocked` and
`evictLocked`). Best-effort: Go's GC may have copied the slice, and that caveat
is documented rather than pretended away.

An entry still being built is never evicted — its rebuild goroutine owns it —
so `curBytes` may transiently sit over budget while builds are in flight, and
catches up on the next eviction pass (`provider.go:294`).

# The leak oracle

`internal/mount/leak_oracle_test.go` is a standing tripwire, not an ordinary
test: sentinel secrets planted in `testdata/` must never appear in any byte read
through a mount, in any test. Treat a failure there as a release blocker.
