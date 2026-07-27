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
