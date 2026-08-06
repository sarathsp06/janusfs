# Bolt's Journal - Critical Learnings

## 2026-07-20 - Ultra-Fast Pre-filtering & Allocation Bypass for Regexp-heavy FUSE Adapters
**Learning:** Regexp matching in Go's standard library is incredibly expensive and allocates a large number of slice headers and structures. When a filesystem adapter (like JanusFS) scans files continuously for secrets on every read, invoking regexes on 100% clean files introduces a massive CPU bottleneck. Furthermore, eagerly cloning file buffers (`bytes.Clone`) in anticipation of modifying them introduces a substantial memory allocation footprint even when no changes are made.
**Action:** Always pre-filter files using extremely fast, allocation-free substring or byte-containment checks (e.g., checking for `=` before invoking an environment-variable regex) before delegating to full regex searches. Additionally, only copy or clone buffers lazily when a modification span is actually found, ensuring clean files consume exactly 0 bytes in allocations.

## 2026-07-27 - Zero-Allocation Span Sorting & In-Place Coalescing
**Learning:**
Standard reflection-based sorting in Go (`sort.Slice`) introduces unwanted memory allocations and overhead inside hot-path FUSE operations. Similarly, allocating a brand-new slice to coalesce and merge overlapping redaction ranges (`Span` lists) increases allocation rates and GC pressure.
Using modern Go (Go 1.21+) generics like `slices.SortFunc` performs in-place sorting completely allocation-free. Furthermore, we can coalesce/merge sorted ranges entirely in-place within the original slice itself, totally eliminating new slice allocations.

**Action:**
Replaced `sort.Slice` with `slices.SortFunc` and `cmp.Compare` to avoid reflection and allocation during span sorting. Refactored `mergeSpans` to filter and coalesce overlapping spans in-place, which reduces total redaction heap allocations for a 1 MB corpus by roughly 30%.

## 2026-07-27 - High-Performance Zero-Allocation Process Identity Retrieval & Ancestry Consolidation

**Learning:**
Retrieving process start times and PPIDs via successive calls to `/proc/<pid>/stat` (or separate sysctl commands on macOS) in ancestry-walk loops introduces massive CPU latency and memory allocation overhead. Splitting stat files on whitespace (using `strings.Fields`) forces the heap allocation of dozens of unused strings for every traversal level.
Furthermore, very fast parent lookup routines can outrun a spawned test subprocess's `execve` transition, causing race conditions in `environ` checks before the environment block is fully populated.

**Action:**
Unified PPID and Start Time lookups into a single `parentAndStartTime` function, cutting OS file reads and system calls in half during ancestry walks. Built a high-performance, single-pass byte parser on Linux that reads `/proc/<pid>/stat` directly into a stack-allocated byte buffer using the low-level `unix` syscalls, achieving exactly zero heap allocations on the parse path. This reduced lookup times by over 56% and slashed allocations by 98.5%.
Replaced the brief fixed delay in process-spawning tests with a condition-based wait for `JANUSFS_SESSION` to become visible in the child's environment before exercising the environ path.

## 2026-07-28 - Zero-Allocation Ancestry and Path-Matching Bypass in Decision Engine

**Learning:**
Rule resolution and ancestor walks are highly critical hot-path operations within a FUSE filesystem. Eagerly building ancestor path segments via `strings.Split`/`strings.Join` and performing absolute path checks (`filepath.Join`, `filepath.Rel`) inside ancestry loops results in high system call rates, excessive memory allocation, and severe CPU cache/GC pressure.
By caching boolean global status (`IsGlobal`) and relative slash-separated paths (`RelDir`) during the rule discovery phase, path matching during resolution can be converted into cheap, allocation-free string prefix and slicing checks. Furthermore, in-place index scanning of slashes avoids segment slice allocations completely.

**Action:**
Added `IsGlobal` and `RelDir` to `IgnoreLevel` and `MaskLevel`. Refactored `Resolve` to walk ancestors in-place using slash-index scanning, and converted applicable level matching to allocation-free string prefix/equality checks. Slashed `BenchmarkResolveCacheMiss` memory allocations by 24% and allocation counts by over 55%, speeding up directory-miss resolutions by approximately 40%.

## 2026-08-06 - Eliminating Level-Filtering Intermediate Slices & Struct Copying in Hot Resolution Path

**Learning:**
Collecting applicable policy levels (e.g. `IgnoreLevel` and `MaskLevel` structs) during rule resolution by building filtered slices dynamically (via helper functions like `applicableIgnoreLevels` and `applicableMaskLevels`) results in significant heap allocations and GC overhead. Furthermore, because these slices contain whole `IgnoreLevel`/`MaskLevel` structs rather than pointers, iterating over them copies large structs with multiple fields on each loop step.
Replacing these helper functions with direct index-based iteration over the original levels slice in `RuleSet` and taking pointer references (`&rs.IgnoreLevels[i]` / `&rs.MaskLevels[i]`) completely eliminates intermediate slice allocations and avoids struct copying.

**Action:**
Removed `applicableIgnoreLevels` and `applicableMaskLevels` from `internal/rules/resolve.go`. Updated both `Resolve` and `resolveIgnore` to directly loop over `rs.IgnoreLevels` and `rs.MaskLevels` by index, checking `isApplicable` on pointers. This optimization reduced `BenchmarkResolveCacheMiss` memory allocations by **86.2%** (from 28,706 B/op to 3,950 B/op) and allocation count by **38.8%** (from 103 to 63 allocs/op), while improving resolution latency by **18%**.
