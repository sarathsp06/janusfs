# PRP 19 — `doctor` diagnoses the container/FUSE composition case

**Size:** S · **Blocked by:** nothing · **Status:** done

## Why

The recommended deployment after repositioning is composition:
`janusfs exec` *inside* a container/sandbox. The failure modes there are
opaque (`/dev/fuse` missing, userns denied) and the fixes live on the
container runtime's command line, not inside the box. `doctor` should name
the exact remedy.

## Change

`internal/health/doctor_linux.go` (new; `doctor_other.go` narrowed to
`!darwin && !linux`), hooked via `platformWarnings()` in `Run`:

- `/dev/fuse` missing + `/.dockerenv` or `/run/.containerenv` present →
  "re-run the container with `--device /dev/fuse`".
- `/dev/fuse` missing outside a container → install fuse3 / load module.
- neither `fusermount3` nor `fusermount` on PATH → install fuse3.
- `kernel.unprivileged_userns_clone=0` or `user.max_user_namespaces=0` →
  named sysctl remedy, with the container-side hint when applicable.

macOS and other platforms return no extra warnings (macFUSE probe already
covers darwin).

## Acceptance

`GOOS=linux go vet` clean; health unit tests green; each warning names its
remedy verbatim rather than a symptom.
