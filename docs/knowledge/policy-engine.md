---
type: Subsystem
title: Policy engine
description: How .janusfs.yml becomes one Decision per path, including the two-tier precedence floor.
tags: [rules, engine, precedence, gitignore]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: rules
    resource: /internal/rules/rules.go
    title: discovery, parsing, compilation
  - id: resolve
    resource: /internal/rules/resolve.go
    title: RuleSet.Resolve, precedence
  - id: engine
    resource: /internal/engine/engine.go
    title: atomic snapshot, generations
  - id: patterns
    resource: /internal/patterns/patterns.go
    title: builtin pattern library
---

# The three faces

`rules.Decision` is a `uint8` enum with exactly three values
(`internal/rules/rules.go:53`): `Allowed`, `Masked`, `Hidden`. `String()`
renders them as `ALLOWED`/`MASKED`/`HIDDEN`, and those strings are the wire
vocabulary used by events, the API, and the dashboard.

`internal/engine` re-exports the type and constants
(`internal/engine/engine.go:27`) so callers never import `internal/rules` just
to name a decision. The enum lives in `rules` because it cannot be defined
correctly anywhere other than next to `Resolve`.

# Compilation

`rules.Discover(root)` builds an immutable `*RuleSet`:

1. load the global level at `~/.janusfs/config` (`GlobalDir()`);
2. `filepath.WalkDir` the whole source tree collecting directories, then sort
   them lexicographically — which is also shallowest-first, because an
   ancestor path is a string prefix of its descendants;
3. for each directory, load `.janusfs.yml`, compiling `hide`/`allow` into an
   `IgnoreLevel` and `mask` into a `MaskLevel`.

Discovery never returns a nil `RuleSet`. Per-file problems accumulate in
`DiscoverErrs` and *also* fail closed; only an unwalkable root is fatal.

A malformed `.janusfs.yml`, unsupported `version`, or hide/allow glob that fails
compile sets `IgnoreLevel.Poisoned`, which folds every path that level covers to
`Hidden`. The reasoning is unchanged: a hide rule only ever widens what is
hidden, so a rule that could not be evaluated must be read conservatively rather
than skipped.

`mask` rules list `paths` and optional `patterns`; omitted patterns mean the
`whole-file` sentinel. Pattern references may be builtin names or `/RE2/` custom
regexes. A glob or pattern that fails to compile sets `MaskEntry.CompileErr`
while still populating `Glob`, so `janusfs check` can report which path is
affected.

Gitignore matching is implemented in-house in `internal/rules/glob.go`, not via
a library. The rationale is recorded at `rules.go:1`.

## Case folding

`RuleSet.FoldCase` (`rules.go`) records whether the source root's backing
volume treats two spellings of a name as the same file — the APFS/HFS+ default
on macOS. `Discover` probes this once via `caseInsensitiveVolume` in
`internal/rules/casefold.go` (a cheap case-flip-and-`os.SameFile` check, no
write access needed) and every pattern in the rule set is compiled against that
one setting via `compilePatternFold`, which prepends `(?i)` to the compiled
regex when folding.

This matters because the kernel and the policy engine must agree about path
identity: on a case-insensitive volume, `cat .ENV` resolves to the real `.env`
inode regardless of what the policy engine thinks, so a case-sensitive `*.env`
mask rule would be trivially evaded by a differently-cased spelling. Folding is
tied to the volume, not to a global preference, because `!`-negation lines
widen visibility and a blanket case-insensitive mode would not be uniformly
fail-closed on a genuinely case-sensitive volume.

`compilePattern` (case-sensitive, `foldCase=false`) still exists as a thin
wrapper over `compilePatternFold` — it is what the test suite uses directly
when a test needs to pin case sensitivity regardless of the machine it runs on.

# Resolution and precedence

`RuleSet.Resolve(relPath, isDir)` (`resolve.go:49`) is pure, never errors, and
folds any internal inconsistency to `Hidden`. Order of operations:

1. **Ancestor short-circuit.** For each ancestor directory of the path,
   shallowest first, run the ignore evaluation. A hidden ancestor returns
   `Hidden` immediately, so no deeper rule can resurface anything beneath a
   hidden directory (`resolve.go:55`).
2. **The path's own ignore evaluation.** Hidden wins immediately.
3. **Directories stop here.** `isDir == true` returns `Allowed` or `Hidden`,
   never `Masked` (`resolve.go:66`). A mask glob matching a directory is
   understood as applying to the files inside it.
4. **Mask accumulation.** Every applicable mask level is scanned; matching
   entries union their patterns, deduplicated by `Pattern.Name`. Any entry with
   a `CompileErr` poisons the result to `Hidden` (`resolve.go:111`).
5. Patterns are sorted by name so `PatternNames[i]` and `Patterns[i]` stay
   parallel and the resulting set has a stable identity (`resolve.go:116`).

## The two-tier floor

`resolveIgnore` (`resolve.go:145`) implements gitignore's "later match wins"
with one deliberate change. The global level (`~/.janusfs/config`) is a
**fail-closed floor**: once the global tier's own self-consistent evaluation
decides a path is hidden, no in-tree `.janusfs.yml` `allow` rule may negate it.

Allow rules still work normally *within* the in-tree tier (deeper files override
shallower ones) and *within* the global level itself (a later global rule
overrides an earlier one). Only an in-tree actor lifting a global verdict is
blocked, because the in-tree files live inside the tree the agent can see.

A blocked negation is not silently dropped: it is recorded in the trace with
`Matched: false, Negated: true` (`resolve.go:173`) so `janusfs check` and
`janusfs explain` can show the user that their `!` line was ignored and why.

# Explainability

Every `Resolve` returns a `[]TraceEntry` alongside the decision
(`resolve.go:14`): file, line number, kind (`ignore` / `mask` /
`config_error`), the raw line, whether it matched, and whether it was a
negation. `RuleRef` is the `"<file>:<line>"` of the deciding rule. This is what
makes `janusfs explain <path>` able to show the derivation rather than just the
verdict.

`Poisoned` marks a decision that became `Hidden` because of a config error
rather than a rule, so the UI can distinguish "you hid this" from "your config
is broken".

# Generations and hot swap

`engine.Engine` holds `atomic.Pointer[rules.RuleSet]` plus an
`atomic.Uint64` generation counter (`engine.go:55`). `Reload(root)`
recompiles off-thread and stores the new snapshot, then increments the
generation (`engine.go:99`). A reader in `Resolve` sees either the old or the
new snapshot, never a half-built one, and never waits.

The generation number is part of the redaction cache key, so a generation bump
invalidates cached redactions implicitly, and `mountRuntime.reload()` also
calls `provider.InvalidateAll()` explicitly
(`cmd/janusfs/runtime.go:53`).

There is no file watcher. Reload happens only on `janusfs update`, the
dashboard's Reload button, or a config save through the dashboard editor. What
makes that safe is that content-change detection was never the watcher's job —
see the cache key in [masking pipeline](masking-pipeline.md).

# Cost per operation

`rules.RuleSet.Resolve` itself is still O(applicable levels × patterns per
level), with a `filepath.Rel` call per level (`relativeToLevel`,
`resolve.go:212`), and `applicableIgnoreLevels`/`applicableMaskLevels` linearly
scan *all* levels in the rule set to find the applicable ones
(`resolve.go:221`). The FUSE adapter calls `resolve()` at least once per
operation, and `Read` calls it on every single read.

`engine.Engine.Resolve` (`engine.go`) memoizes that cost: a `sync.Map` keyed by
`decisionKey{relPath, isDir, gen}` caches the full `Resolution`, so a repeat
resolution of the same path against the same rule-set generation is a single
map lookup rather than a hierarchy walk. Measured on this development machine
(Apple M3 Pro): a cache hit is ~55 ns, a ten-level cache miss is ~90 µs — both
within NFR-3's budget (≤ 5 µs hit, ≤ 200 µs miss), recorded in
`bench/BASELINE.md`.

Putting the generation inside the cache key, rather than checking it
separately, is what makes invalidation free: `Reload` only needs to increment
the generation counter (and drop the map, to actually release the now-orphaned
entries' memory rather than merely making them unreachable) — no sweep, no
per-entry invalidation logic. The one subtlety worth preserving if this code is
touched again: `Resolve` loads the generation *before* the rule set, not after,
specifically because loading it after would let a concurrent `Reload` race in
between and cache an old-snapshot decision under the new generation's key — a
stale-serve bug reachable only under concurrency, not something a single-
threaded test would catch. The reverse ordering's failure mode (a
new-snapshot decision cached under the old, soon-unreachable generation key) is
merely a wasted entry, never a stale serve.

The cache is bounded (`decisionCacheMax`, 100,000 entries) by dropping the
whole map rather than maintaining an LRU, since an untrusted caller can `stat`
unlimited distinct nonexistent paths and each one produces an entry — see the
`ponytail:` comment at its definition for the upgrade path if profiling ever
shows the drop causing real churn.
