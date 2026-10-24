# PRP 18 — Loud warning for the `git add` masked-bytes hazard

**Size:** M · **Blocked by:** nothing · **Status:** done

## Why

The one place JanusFS *causes* data loss (SPEC accepted-risk FR-31a): inside
an enforced view, `git add` of a Masked file stages `****` into the real
object store. SPEC's own text says a mitigation must exist before enforcement
is marketed to commit-capable agents — exactly what PRP 17 markets.

## Change

- `internal/check/githazard.go` (new): `GitStagingHazards(root)` — intersects
  `git ls-files --cached --others --exclude-standard` with Masked decisions
  via `internal/engine`. `(nil, nil)` when git/work-tree absent: advisory,
  never blocks. Shells out to git — called only from short-lived CLI paths,
  never the daemon or `conflicts.json`.
- `janusfs check`: each hazard is a `warn` finding with the .gitignore
  suggestion (wired in `cmd/janusfs/check.go`, not in `check.Run`, so the
  daemon-side conflicts.json path stays exec-free).
- `janusfs exec` (both platforms): loud stderr warning before the child
  starts, listing up to 10 paths + count.

## Deliberately not done

Blocking `git add` itself (write-deny of `.git/index` inside exec views) —
requires per-writer detection the thin mount layer cannot do; a default-deny
of `.git/objects` writes would break `git status`/`log` refresh paths. If
the warning proves insufficient in practice, that stronger mechanism gets
its own PRP with a design section.

## Acceptance

Unit test: masked+stageable file reported; allowed and gitignored files not;
non-git dir yields nil. Leak oracle unaffected.
