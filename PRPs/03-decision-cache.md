# PRP 03 — Decision cache and explicit cache policy

**Size** S · **Blocked by** nothing · **Touches** `internal/engine`,
`internal/mount`

## Goal

Make a repeat decision lookup cost ~1 µs instead of a full hierarchy walk, and
turn the current accidental FUSE cache behaviour into a stated, tested decision.

## Why

Two reasons, and the second is why this is sequenced early.

**Resolution is recomputed constantly.** `RuleSet.Resolve` linearly scans *every*
level in the rule set to find the applicable ones
(`internal/rules/resolve.go:221`), computes a `filepath.Rel` per applicable level
(`:212`), and walks that level's patterns. There is no memoization anywhere. The
adapter then calls it more than once per operation: `resolve()` per op,
`decisionFor` plus an `os.Lstat` per mutation op, and `maskedHandle.Read` resolves
on **every single read** (`internal/mount/janus_node.go:541`) — deliberately, for
correctness after a reload. The stated budget is 5 µs for a cache-hit decision,
which cannot be met, because there is no cache.

**It buys back the budget that identity will spend.** PRP 06 adds at least one
syscall per operation. Doing that on top of an unmemoized hierarchy walk stacks
two costs where one was already over budget. Land the cache first so 06's
benchmark measures identity's real marginal cost.

## Context

- Resolution cost, with anchors:
  [`docs/knowledge/policy-engine.md`](../docs/knowledge/policy-engine.md), final
  section
- Cache invariants:
  [`docs/knowledge/masking-pipeline.md`](../docs/knowledge/masking-pipeline.md)
- Requirements: [SPEC.md NFR-3](../SPEC.md#4-non-functional-requirements),
  [FR-34](../SPEC.md#35-isolation-and-path-parity)
- Gap register item 6:
  [`known-gaps.md`](../docs/knowledge/known-gaps.md)

Verified API facts:

- `fs.Options` embeds `fuse.MountOptions` (`fs/api.go:764`), so `opts.DirectMount`
  and friends are reachable directly on the `opts` value the adapter already
  builds.
- `fs.Options.EntryTimeout`, `AttrTimeout`, and `NegativeTimeout` are all
  `*time.Duration` (`fs/api.go:768`–`780`). All three are **nil** today in both
  `mount_darwin.go:87` and `mount_linux.go:77`, which is why real files currently
  get no kernel dentry or attribute caching.

---

## Task 1 — Memoize `Engine.Resolve`

**File** `internal/engine/engine.go`

The key must include the generation. That is what makes invalidation free: a
reload bumps the generation, every existing entry becomes unreachable, and no
sweep, no eviction pass, and no lock-step with `provider.InvalidateAll()` is
needed.

```go
type decisionKey struct {
	relPath string
	isDir   bool
	gen     uint64
}
```

Place the cache on `Engine` (`engine.go:55`), beside the existing
`atomic.Pointer[rules.RuleSet]` and `atomic.Uint64` generation.

Concurrency: a plain `sync.Map` is the right first choice here. Reads dominate
overwhelmingly, keys are write-once (a `(path, gen)` pair's answer never changes),
and it avoids the single-mutex contention a `map` + `RWMutex` would add to every
FUSE handler.

```go
func (e *Engine) Resolve(relPath string, isDir bool) Resolution {
	gen := e.gen.Load()
	k := decisionKey{relPath, isDir, gen}
	if v, ok := e.cache.Load(k); ok {
		return v.(Resolution)
	}
	rs := e.rs.Load()
	res := /* ... existing conversion ... */
	e.cache.Store(k, res)
	return res
}
```

Two things that must be right:

**Load the generation once, before resolving.** If you read `e.gen` after
`e.rs.Load()`, a concurrent `Reload` between the two can cache a decision from
the *old* snapshot under the *new* generation, which is a stale-policy bug that
will be extremely hard to reproduce.

**`Resolution` must be safe to share.** It contains `PatternNames []string`,
`Patterns []*patterns.Pattern`, and `Trace []rules.TraceEntry`. Cached entries are
handed to many concurrent readers, so no caller may mutate them. Verify no caller
does — grep the consumers of `Resolution` — and if any appends to a returned
slice, fix that caller, not the cache.

**Bound it.** An adversarial or merely enthusiastic agent can `stat` unlimited
distinct nonexistent paths, each producing a cache entry, so an unbounded map is
a memory-growth vector reachable from the untrusted side. Drop the whole cache
when it exceeds a fixed entry count — the simplest bound that works, and correct
because a cold cache is a performance question, not a correctness one:

```go
// ponytail: whole-cache drop past a fixed entry ceiling, not an LRU. Decisions
// are cheap to recompute and the generation key already makes reload-invalidation
// free; add an LRU only if profiling shows the drop causing real churn.
const decisionCacheMax = 100_000
```

Track the count with an `atomic.Int64` incremented on store; on overflow, replace
the `sync.Map` wholesale (guarded so two goroutines don't both replace it).

Also drop the cache in `Reload` (`engine.go:99`) even though the generation key
already makes old entries unreachable — otherwise they are unreachable but still
retained, which is a leak across many reloads.

**Test** `internal/engine/engine_test.go`:

- hit returns the same decision as a miss, for allowed, masked, and hidden paths;
- after `Reload`, a path whose rule changed returns the **new** decision, proving
  the generation key works;
- run the existing `TestEngineConcurrentResolveWithReload` under `-race` — it
  already exercises resolve-during-reload and is the main safety net here;
- exceeding `decisionCacheMax` does not grow without bound and does not return a
  wrong answer.

**Benchmark**, and this is the point of the task, so it is not optional:

```go
func BenchmarkResolveCacheHit(b *testing.B)   // target: ≤ 5 µs
func BenchmarkResolveCacheMiss(b *testing.B)  // 10-level hierarchy, target: ≤ 200 µs
```

Record both in `bench/BASELINE.md`.

---

## Task 2 — State the FUSE cache policy explicitly

**Files** `internal/mount/mount_darwin.go:87`, `internal/mount/mount_linux.go:77`

Today all three timeout fields are nil, so real files are revalidated on every
lookup. That is the correct conservative behaviour for a filesystem whose answers
change on a policy reload — but it is currently an accident of omission, and an
accident is not a guarantee. Someone will "optimise" it later without knowing why
it was zero.

**Change**: set them explicitly to zero, with a comment stating the reason.

```go
// Zero attribute, entry, and negative-lookup timeouts: a policy reload must take
// effect on the next lookup, so the kernel is never allowed to answer from a
// cached dentry or attribute. This costs an upcall per lookup and that is the
// intended trade — a stale cached answer would serve the previous policy.
zero := time.Duration(0)
opts.AttrTimeout = &zero
opts.EntryTimeout = &zero
opts.NegativeTimeout = &zero
```

Do **not** change the synthetic `.janusfs` nodes, which correctly cache for an
hour (`janus_node.go:160`, `janus_virtual.go:59`) — their content is generated,
not policy-governed.

Do **not** attempt per-caller cache policy. FUSE page and dentry caches are keyed
by inode in the kernel with no notion of which process caused the fill, so
"cached for host tools, uncached for agents" is not implementable, and an
implementation that appears to work is leaking redacted or unredacted pages
across contexts. This is recorded as rejected in
[SPEC.md §20](../SPEC.md#20-risks-and-rejected-designs); the comment above should
not restate it, but the reviewer should know it was considered.

**Test** `internal/mount/`: assert the constructed `fs.Options` has all three
timeouts non-nil and zero. A behavioural assertion needs a real mount, so keep it
in the `fuseintegration`-tagged suite: modify a `.janusmask`, call reload, and
assert the next `open` of an affected path reflects the new decision without a
remount.

---

## Validation

```bash
rtk make verify
rtk go test -race ./internal/engine/... -run Concurrent -count=4
rtk make bench            # must show the cache-hit target met
rtk make leak-oracle      # decisions now come from a cache; prove nothing leaked
```

The leak oracle matters more than usual here. A caching bug in a policy engine
looks exactly like a leak, and the oracle is the check that would catch a
`(path, gen)` key collision serving the wrong decision.

## Done when

- [ ] `BenchmarkResolveCacheHit` meets ≤ 5 µs, recorded in `bench/BASELINE.md`
- [ ] A post-`Reload` resolve returns the new decision, asserted by test
- [ ] `-race` clean with concurrent resolve and reload, `-count=4`
- [ ] The cache is bounded, with the ceiling and its upgrade path in a
      `ponytail:` comment
- [ ] All three FUSE timeouts explicitly zero, with the reason in a comment
- [ ] Leak oracle green
- [ ] Item 6 removed from [`known-gaps.md`](../docs/knowledge/known-gaps.md); a
      line appended to [`log.md`](../docs/knowledge/log.md)

## If this is wrong

- Assumes `Resolution`'s slices are never mutated by callers. If any caller does
  mutate one, sharing cached entries corrupts other callers' results. Grep before
  implementing; if you find a mutator, fix the caller and say so in the commit
  message.
- Assumes `sync.Map` beats `map` + `RWMutex` for this access pattern. If the
  benchmark says otherwise, use whichever wins and record the number.

## Anti-patterns

- Do not cache in `internal/rules`. `RuleSet` is an immutable snapshot with no
  concept of a generation; the cache belongs with the thing that owns generations.
- Do not remove `maskedHandle.Read`'s per-read re-resolve. It is what makes the
  system correct across a reload, and with this cache it is now cheap.
- Do not add a TTL. The generation is the invalidation mechanism; a TTL would add
  a window where stale policy is served.
- Do not use non-zero FUSE timeouts to hit a benchmark number. The upcall per
  lookup is the price of correct reload semantics.
