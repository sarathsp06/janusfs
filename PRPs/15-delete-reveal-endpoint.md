# PRP 15 — Delete `/api/v1/reveal` and the dashboard view/edit UI

**Size:** S · **Blocked by:** nothing · **Status:** done

## Why

A secrets-redaction product serving **raw source bytes and remote file edits
over loopback HTTP** is a self-undermining surface: it converted "leak a
token" into "read and rewrite any file under the mount root". The operator
has an editor; the dashboard doesn't need to be one. This closes the
structural half of the confined-child reachability gap.

## Change

- `internal/api/server.go`: route, `handleReveal`, `maxRevealWrite` deleted.
- `internal/api/api_test.go`: `TestRevealViewAndEdit` deleted.
- `internal/ui/index.html`: `revealFile`/`saveFile` and the per-file
  "view / edit" toggle deleted. CodeMirror stays (config editor uses it).
- Comment sweep: no reveal mention remains in `internal/ui`/`internal/api`.

## Acceptance

`GET /api/v1/reveal?...` is 404; `grep -ri reveal internal/` returns nothing
load-bearing; api tests green.
