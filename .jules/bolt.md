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

## 2026-08-09 - Zero-Allocation Cache Hit Fast-Path in FUSE RAM Cache

**Learning:**
Generating a stable, order-independent signature string (`patternSignature`) for every single cache lookup in `RamCache.ReadAt` introduces significant CPU latency and memory allocation overhead. Under realistic hot-path workloads where files are read sequentially and patterns are unchanging, invoking `strings.Builder` and slice/sorting helpers for clean cache-hit paths generates unnecessary GC pressure.

By checking if the entry is already built and evaluating signature matching directly via a stack-allocated array (for up to 8 patterns) with a zero-allocation parsing approach (`patternMatchesSig`), we can completely bypass signature generation, channel creations, context checks, and lock-handling overhead.

**Action:**
Implemented `patternMatchesSig` using a local stack-allocated array for up to 8 patterns to perform sorted, zero-allocation comparison with the existing signature string. Optimized the cache-hit path in `RamCache.ReadAt` to evaluate this helper under the first lock. If a hit is verified and built, the cached bytes are copied and returned immediately. Slashed `BenchmarkReadAtCacheHit` from 749.9 ns/op down to 64.44 ns/op (a ~11.6x speedup) and reduced memory usage to exactly 0 B/op and 0 allocs/op (a 100% reduction).
