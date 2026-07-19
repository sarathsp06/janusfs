// Package apperrors defines the canonical sentinel errors for JanusFS and the
// single translation point from any of them to a syscall.Errno (SPEC §13's
// error-handling matrix). internal/mount is the only package permitted to
// call ToErrno (SPEC §21); every other package returns one of these
// sentinels (wrapped with context via %w) and lets the mount adapter
// translate it.
//
// Reintroduced here (removed in an earlier YAGNI pass when nothing yet
// produced these conditions) now that internal/provider/internal/mount
// actually generate them: a cache rebuild can time out, a redaction can hit
// an unsupported oversized/unbounded case, and a symlink can point outside
// the source tree. Scoped to exactly the rows those packages hit — see
// SPEC.md §13 for the full canonical table this is one piece of.
package apperrors

import (
	"errors"
	"syscall"
)

var (
	// ErrRebuildTimeout is returned when a masked-content cache rebuild
	// does not complete within its bound (FR-20: 10 s) — errno EIO.
	ErrRebuildTimeout = errors.New("apperrors: rebuild timeout")

	// ErrRedactUnsupported is returned when a file cannot be safely
	// redacted: an unbounded custom regex on a file exceeding
	// --redact-buffer-max (§8.2) — errno EACCES (fail closed, same as a
	// masked file with no readable content).
	ErrRedactUnsupported = errors.New("apperrors: redaction unsupported for this file")

	// ErrSymlinkEscape is returned when a symlink's target resolves
	// outside the source tree (FR-10) — errno ENOENT on follow, so the
	// mount never becomes an escape hatch to unprotected paths.
	ErrSymlinkEscape = errors.New("apperrors: symlink escapes source tree")

	// ErrPanic marks a FUSE handler panic recovered by the adapter
	// (NFR-6) — errno EIO.
	ErrPanic = errors.New("apperrors: panic recovered")
)

// ToErrno maps one of this package's sentinels (matched via errors.Is) to
// the syscall.Errno SPEC §13's table assigns it. internal/mount is the only
// caller (SPEC §21); every other package returns a sentinel and lets this
// function do the translation, keeping the errno mapping in exactly one
// place. An err that isn't one of these sentinels maps to EIO — an
// unexpected internal error is treated the same as any other "something
// went wrong, fail closed" condition (SPEC §20.2), never leaked as some
// other errno that might imply a more specific (and wrong) cause.
func ToErrno(err error) syscall.Errno {
	switch {
	case errors.Is(err, ErrSymlinkEscape):
		return syscall.ENOENT
	case errors.Is(err, ErrRedactUnsupported):
		return syscall.EACCES
	case errors.Is(err, ErrRebuildTimeout), errors.Is(err, ErrPanic):
		return syscall.EIO
	default:
		return syscall.EIO
	}
}
