# PRP 10 — Linux CI verification of PRP 04

**Size:** S · **Blocked by:** nothing · **Status:** done (CI gates every PR)

## Why

The entire defensible story of JanusFS — kernel-enforced `janusfs exec` on
Linux — shipped in PRP 04 but was authored on a darwin machine and never
executed against a real kernel. Every downstream claim (README, positioning,
harness recipes) is marketing until the `fuseintegration` suite runs on Linux.

## What broke while nobody was looking

A dead-code pass (`d14c827`) deleted `Adapter.Unmount`, which only the
tag-gated tests referenced. `make integration` on Linux CI failed to *compile*
from 2026-08-29 onward. Lesson encoded below.

## Change

1. `internal/mount/mount.go`: restore `Adapter.Unmount` (used by tagged test
   cleanup on both platforms).
2. `.github/workflows/ci.yml`: add a `go vet -tags fuseintegration ./...` step
   so the tagged tree can never silently rot again. CI already runs
   `make integration` on `ubuntu-latest`.
3. `internal/mount/overmount_linux_test.go` (new, `linux && fuseintegration`):
   the spike PRP 04 deferred — mount the adapter directly OVER its own source
   (`Mount(ctx, src, src)`, no shadow bind) and read through it with a
   deadline. Passing proves the nsmount shadow bind mount is removable
   (simplification, tracked as follow-up); hanging proves it load-bearing.
   Either result is recorded by the test's failure/log text.

## Validation

- `go vet -tags fuseintegration ./...` green for darwin and `GOOS=linux`.
- Green `fuseintegration` suite on `ubuntu-latest` on every PR from now on.
- Update `docs/knowledge/platform-isolation.md`'s "unverified" language and
  the PRP 04 caveat in this directory's README once the first green run lands.

## If this is wrong

If the ubuntu-latest runner cannot complete FUSE mounts (kernel or permission
regression on GitHub's images), the fallback is a self-hosted or container
job with `--device /dev/fuse`; do not mark PRP 04 verified from a skip.
