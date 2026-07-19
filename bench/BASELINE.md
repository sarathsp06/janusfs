# Phase 0 Baseline

Captured by `bench/run_spike.sh` (SPEC.md §6/§24 spike acceptance list). This file is the source of truth for the NFR-3 performance budget's percentage thresholds — later phases compare against the numbers recorded here, not against a guess.

**Status: not yet captured.** Blocked on FUSE-T being installed on the development machine (`brew install --cask fuse-t` requires an interactive sudo password, so it can't be run from an unattended session — see `docs/DEV_LOG.md`).

Once captured, this section will record:
- Raw FUSE passthrough baseline: sequential read/write throughput for a 1024 MiB file, native (no JanusFS) vs. through the passthrough mount.
- Spike acceptance list result (go/no-go) per SPEC §6.
- Any FUSE quirks observed in practice (attribute caching behavior, AppleDouble noise, flock semantics) beyond what SPEC §6 already anticipates.

## Phase 2 — redaction throughput (measured, gate not yet verified)

`internal/redact.Redact` on a 1 MB synthetic dotenv-like corpus (20,000 lines, `env-value` pattern), Apple M3 Pro, `go test -bench`:

```
BenchmarkRedactDotenvLike-12    22  50616491 ns/op  22.13 MB/s  5964739 B/op  20066 allocs/op
```

**Below NFR-3's 100 MB/s single-threaded target.** The dominant cost is `regexp.FindAllSubmatchIndex`'s one-slice-per-match allocation (≈1 alloc/line on this corpus) — inherent to the stdlib API as currently used, not an obvious quick fix. Per SPEC.md §17, NFR-3 gate verification against this baseline is explicitly a **Phase 4** task ("Latency budget verification... against Phase 0 baselines"); this number is recorded now, honestly, rather than silently deferred. Real-world `.env`/config files are typically a few KB (not 1 MB / 20k lines), so this gap is not expected to be user-visible before Phase 4 — revisit then, or sooner if a real workload shows it matters.
