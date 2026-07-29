# Mount registry cleanup, mount listing, and check matches design

## Context

JanusFS currently has three related UX and lifecycle gaps:

1. The daemon records mount state in `~/.janusfs/mounts.json`, but records can outlive the source directory or a stale/broken mountpoint.
2. `janusfs paths` is easy to confuse with a mount listing command, even though it reports JanusFS config/data paths.
3. `janusfs check` is useful as a linter, but there is no direct way to preview all files that policy currently hides or masks.

This design keeps the existing daemon model and adds the smallest user-facing surface that makes those states visible and self-healing.

## Goals

- Keep `janusfs check <dir>` lint-only by default.
- Add `janusfs check <dir> --matches` to preview policy effects.
- Add `janusfs mounts` to list active and recorded mounts.
- Have daemon startup prune impossible-to-resume `mounts.json` entries.
- Keep `janusfs paths` for config/data path diagnostics, but document it distinctly from `janusfs mounts`.

## Non-goals

- Do not rename or remove `janusfs paths` in this change.
- Do not redesign the daemon/control protocol beyond what `mounts` needs.
- Do not add a continuous watcher.
- Do not print every `ALLOWED` file in `--matches` by default.

## Design

### 1. Daemon registry cleanup

During daemon startup/resume, each record in `~/.janusfs/mounts.json` is classified before resume:

- `missing-src`: `src` no longer exists or is not a directory.
- `stale`: mountpoint appears mounted or broken but is not owned by the current daemon.
- `recorded`: record exists and source exists, but it is not currently active.
- `mounted`: resume succeeded and the daemon owns it.
- `error`: stat/check failed in a way that should be reported but not panic.

Startup behavior:

1. If `src` is missing or not a directory:
   - remove the record from `mounts.json`;
   - remove any pidfile for the recorded mountpoint;
   - prune empty mirror directories under the mount root;
   - log a warning;
   - continue startup.
2. If the mountpoint appears stale/broken:
   - attempt a force-unmount using the existing `unmountKernel(..., force=true)` ladder;
   - if source still exists, attempt normal resume;
   - if resume succeeds, keep the record;
   - if resume fails, remove the record and log a warning.
3. If source exists and mountpoint is usable:
   - resume as today.

This makes `mounts.json` self-healing without blocking daemon startup on bad records.

### 2. `janusfs mounts`

Add a new command:

```bash
janusfs mounts
janusfs mounts --json
```

It lists both active daemon mounts and recorded mount entries. Human output should be table-shaped:

```text
STATUS        SOURCE                    MOUNTPOINT
mounted       /Users/me/app             /Users/me/.janusfs/mounts/Users/me/app
recorded      /Users/me/other           /Users/me/.janusfs/mounts/Users/me/other
missing-src   /Users/me/deleted         /Users/me/.janusfs/mounts/Users/me/deleted
stale         /Users/me/old             /Users/me/.janusfs/mounts/Users/me/old
```

Status meanings:

- `mounted`: active mount owned by the daemon.
- `recorded`: in `mounts.json`, source exists, but not currently active.
- `missing-src`: recorded source path does not exist or is not a directory.
- `stale`: mountpoint still appears mounted/broken but daemon does not own it.
- `error`: classification failed; include an error field in JSON and a short note in human output.

`--json` should return a stable schema suitable for scripts:

```json
{
  "mounts": [
    {
      "status": "mounted",
      "src": "/Users/me/app",
      "mountpoint": "/Users/me/.janusfs/mounts/Users/me/app",
      "label": "",
      "dashboard": "http://127.0.0.1:7381/mounts/.../"
    }
  ]
}
```

Implementation should prefer daemon-reported live state when the daemon is running, and merge it with `mounts.json` records so stale/recorded entries remain visible.

### 3. Keep `janusfs paths`

Keep the existing command, but clarify its help and docs:

```text
janusfs paths   Show JanusFS config/data paths
janusfs mounts  List active and recorded mounts
```

No aliasing or removal in this change.

### 4. `janusfs check <dir> --matches`

Keep default behavior lint-focused:

```bash
janusfs check .
```

Add:

```bash
janusfs check . --matches
janusfs check . --matches --json
```

`--matches` appends a policy preview to the normal lint report. By default it lists only `HIDDEN` and `MASKED` paths, not `ALLOWED` paths.

Human output shape:

```text
Policy matches:

HIDDEN
  server.pem                    .janusfs.yml:4
  .aws/credentials              .janusfs.yml:7

MASKED
  .env                          .janusfs.yml:12  [env-value]
  config/application.yml         .janusfs.yml:18  [generic-secret db-uri]
```

Rules:

- Walk the source tree once, reusing existing `check` tree-walk machinery where possible.
- Include files and directories that resolve `HIDDEN`.
- Include files that resolve `MASKED`.
- Do not include `ALLOWED` by default.
- Preserve existing lint findings and exit-code behavior.
- If `--json` is set, add a `matches` array to the JSON report only when `--matches` is requested.

Possible future extension, out of scope for this change:

```bash
janusfs check . --matches --all
```

which could include `ALLOWED` inventory.

## Error handling

- Registry cleanup must be best-effort. Bad records should never prevent daemon startup.
- Failed cleanup should log warnings and continue.
- `janusfs mounts` should report classification errors per record rather than failing the entire command when possible.
- `janusfs check --matches` should retain current check error behavior: syntax/config errors remain findings and errors, and matching should not hide them.

## Testing

Add unit tests for:

- daemon resume pruning missing-source records;
- stale mountpoint cleanup path using existing mount/unmount seams;
- `janusfs mounts` human output classification;
- `janusfs mounts --json` schema;
- `janusfs check --matches` human output showing Hidden and Masked entries;
- `janusfs check --matches --json` including matches;
- default `janusfs check` staying lint-only.

Run:

```bash
rtk make test
```

If implementation touches real mount behavior, also run the relevant integration/leak-oracle commands when available.
