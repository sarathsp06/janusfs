# Phase 0 Baseline

Captured by `bench/run_spike.sh` (SPEC.md §6/§24 spike acceptance list). This file is the source of truth for the NFR-3 performance budget's percentage thresholds — later phases compare against the numbers recorded here, not against a guess.

**Status: not yet captured.** Blocked on FUSE-T being installed on the development machine (`brew install --cask fuse-t` requires an interactive sudo password, so it can't be run from an unattended session — see `docs/DEV_LOG.md`).

Once captured, this section will record:
- Raw FUSE-T passthrough baseline: sequential read/write throughput for a 1024 MiB file, native (no JanusFS) vs. through the passthrough mount.
- Spike acceptance list result (go/no-go) per SPEC §6.
- Any FUSE-T quirks observed in practice (attribute caching behavior, AppleDouble noise, flock semantics) beyond what SPEC §6 already anticipates.
