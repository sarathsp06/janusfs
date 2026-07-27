---
type: Reference
title: Config and on-disk layout
description: The single Config struct, its four-stage precedence, mountpoint derivation, and every path under ~/.janusfs.
tags: [config, env, paths, validation]
status: stable
generated: { by: claude-code/claude-fable-5, at: 2026-07-26T00:00:00Z }
sources:
  - id: config
    resource: /internal/config/config.go
    title: Config, Default, ApplyFile, ApplyEnv, Validate, ResolveMountpoint
  - id: install
    resource: /cmd/janusfs/install.go
    title: install command, mount-root prompt
  - id: paths
    resource: /cmd/janusfs/paths.go
    title: paths and path commands
---

# The rule

Every tunable named anywhere in the spec has a field on one struct,
`config.Config` (`internal/config/config.go:54`). No package other than
`cmd/janusfs` reads a flag or calls `os.Getenv` directly. Env vars are read only
in `ApplyEnv`.

# Fields and defaults

| Field | Flag | Env | Default |
|---|---|---|---|
| `Src` | positional | — | required |
| `Mountpoint` | positional | — | derived from `MountRoot` if omitted |
| `UIPort` | `--ui-port` | `JANUSFS_UI_PORT` | `7381` |
| `CacheMaxBytes` | `--cache-max-bytes` | `JANUSFS_CACHE_MAX_BYTES` | 256 MB |
| `CacheMaxFile` | `--cache-max-file` | `JANUSFS_CACHE_MAX_FILE` | 64 MB |
| `HistoryRetentionDays` | `--history-retention` | `JANUSFS_HISTORY_RETENTION_DAYS` | 30 |
| `NoHistory` | `--no-history` | `JANUSFS_NO_HISTORY` | false |
| `RedactBufferMax` | `--redact-buffer-max` | `JANUSFS_REDACT_BUFFER_MAX` | 512 MB |
| `MountRoot` | `--mount-root` | `JANUSFS_MOUNT_ROOT` | unset (also from `settings.json`) |

Constants are at `config.go:27`. `Src` and `Mountpoint` are positional only and
have no env equivalent.

# Precedence

`Default()` → `ApplyFile()` → `ApplyEnv()` → flags. The daemon applies exactly
that order (`cmd/janusfs/daemon.go:127`). `ApplyFile` reads
`~/.janusfs/settings.json`, which currently carries one key, `mount_root`
(`config.go:164`). A missing file is not an error; a malformed one is.

Env helpers `envInt`/`envInt64`/`envBool` (`config.go:297`) treat unset **and
empty** as absent, so `JANUSFS_UI_PORT=` does not clobber the default, and a
parse failure is a hard error naming the variable and its value.

# Mountpoint derivation

`ResolveMountpoint()` (`config.go:345`) mirrors the source's full,
symlink-resolved absolute path under `MountRoot`:

```
mount /Users/me/projects/app   with mount root ~/.janusfs/mounts
  →    ~/.janusfs/mounts/Users/me/projects/app
```

`filepath.Join` swallows the leading slash of the absolute source, which nests
the whole path rather than anchoring at it. Every source maps to a unique,
predictable location, so two sources never collide. There is deliberately no
override: `--name` is a dashboard label, not a different path.

This derivation is the reason **the mountpoint is never equal to the source
path**, which is the root of the path-parity problem described in
[exec and path parity](exec-and-path-parity.md).

# Validation

`Validate()` (`config.go:371`) runs before any FUSE call and enforces:

1. `Src` and `Mountpoint` both non-empty;
2. `Src` exists and is a directory;
3. `Mountpoint` exists, is a directory, and is **empty**;
4. `Src` and `Mountpoint` do **not overlap** in either direction.

Messages are written for an operator — no `config:` prefix, no wrapped syscall
text — because the CLI surfaces them directly.

Three exported or sentinel errors carry meaning to callers:
`ErrMountpointNotEmpty` is exported so the daemon can detect a collision and
add a remedy; `errEmptyPath` and `errOverlap` are internal.

`absClean` (`config.go:438`) resolves to an absolute, cleaned,
symlink-evaluated path so overlap checks cannot be fooled by relative
segments, trailing slashes, or symlinks — falling back to the cleaned absolute
path when `EvalSymlinks` fails, since existence is the caller's check.

`pathsOverlap` (`config.go:475`) compares with trailing separators appended, so
`/a/bc` is not treated as being under `/a/b`.

**Note for future work**: rule 4 makes a path-preserving overmount
(`Mountpoint == Src`) impossible today. Any design that mounts over the source
must relax this rule under an explicit mode rather than deleting it.

# The mounts registry

`~/.janusfs/mounts.json`, mode `0600`, a JSON array of
`{src, mountpoint, label}` (`config.go:221`). Keyed by mountpoint:
`RecordMount` upserts by mountpoint (`config.go:264`), `RemoveMount` filters it
out (`config.go:283`), and `LoadMounts` treats a missing file as empty.

This file is the input to daemon `resume()`. An explicit `janusfs umount`
removes the entry so resume does not bring back a mount the user chose to stop.

# Everything under ~/.janusfs

Directory mode `0700`, file mode `0600`, everywhere.

| Path | Written by | Contents |
|---|---|---|
| `daemon.sock` | daemon | control socket (`cmd/janusfs/daemon.go:605`) |
| `settings.json` | `install` | `{"mount_root": "..."}` (`config.go:156`) |
| `mounts.json` | daemon on mount/unmount | resume registry (`config.go:210`) |
| `config/` | `init --global` | machine-wide `.janusignore`/`.janusmask` (`internal/rules/rules.go:44`) |
| `run/<sha256-of-mountpoint>.pid` | daemon per mount | owning PID (`cmd/janusfs/pidfile.go:18`) |
| `history/<basename>-<hash12>.db` | per mount | SQLite rollups (`cmd/janusfs/mount.go:109`) |
| `mounts/<full-src-path>/` | derived mountpoints | the sanitized views themselves |

The history DB filename includes both the basename and the first 12 hex digits
of the source path's SHA-256, so two directories both named `app` cannot share
and corrupt one DB now that the daemon holds many mounts at once.

`~/.janusfs` must never itself be a mount source: the history DB deliberately
writes path names to disk, and mounting the directory that holds it would be
circular.

# Discoverability

`janusfs paths` (`cmd/janusfs/paths.go:57`) prints each of the four
user-relevant paths with `present`/`absent`, and annotates the mount root as
`default, not configured` when nothing has been saved. `janusfs path <src>`
prints just the mountpoint so it composes in a shell:
`cd "$(janusfs path ~/projects/app)"`.
