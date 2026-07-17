// Package apperrors defines the canonical sentinel errors for JanusFS and the
// single translation point from those sentinels to syscall errno values.
//
// SPEC §13 defines the error-handling matrix (condition → errno → event name)
// as binding, executable behavior rather than documentation-only prose: one
// sentinel error per row, plus one ToErrno function. Per SPEC §13/§21 and
// AGENTS.md's code conventions, internal/mount is the only package permitted
// to call ToErrno; every other package returns one of these sentinels (wrapped
// with %w as needed) and lets the mount adapter perform the translation, so the
// errno mapping lives in exactly one place.
package apperrors

import (
	"errors"
	"syscall"
)

// Sentinel errors corresponding one-to-one to the rows of SPEC §13's
// error-handling matrix.
var (
	// ErrHidden corresponds to "Read/write/open on HIDDEN" (SPEC §13): the
	// path resolves to the HIDDEN decision (SPEC §7, FR-5) and any access
	// must be denied. Maps to EACCES, event "denied".
	ErrHidden = errors.New("apperrors: path hidden")

	// ErrMaskedReadOnly corresponds to "Write/open-for-write on MASKED"
	// (SPEC §13): masked paths serve redacted read-only content (FR-7); any
	// write or open-for-write attempt is denied. Maps to EACCES, event
	// "denied".
	ErrMaskedReadOnly = errors.New("apperrors: masked path is read-only")

	// ErrRebuildTimeout corresponds to "Rebuild timeout (10 s)" (SPEC §13):
	// the redacted-content rebuild for a stale cache entry (SPEC §8.3,
	// FR-20) did not complete within the allotted window. Maps to EIO,
	// event "rebuild_timeout".
	ErrRebuildTimeout = errors.New("apperrors: rebuild timeout")

	// ErrPanic corresponds to "Redactor invariant violation / handler panic"
	// (SPEC §13, NFR-6): a FUSE handler or the redactor recovered from a
	// panic (e.g. the len(out)==len(in) invariant, SPEC §8.2, was violated).
	// Maps to EIO, event "panic".
	ErrPanic = errors.New("apperrors: panic recovered")

	// ErrConfigParse corresponds to "Config parse failure (per FR-13)"
	// (SPEC §13). This row is informational only: it does not carry its own
	// errno mapping because a config parse failure always folds a path to
	// HIDDEN (FR-6/FR-13) — callers should return ErrHidden for the actual
	// access, and may use ErrConfigParse purely to select the "config_error"
	// event name for observability (SPEC §10) when reporting the underlying
	// cause. ToErrno maps it to EACCES (same as ErrHidden) since that is the
	// fail-closed outcome any config-parse-triggered access must produce.
	ErrConfigParse = errors.New("apperrors: config parse failure")

	// ErrSymlinkEscape corresponds to "Symlink escaping <src>" (SPEC §13):
	// a symlink target resolves outside the source tree root, which must
	// never be followed (SPEC §14 security model). Maps to ENOENT on
	// follow, event "symlink_escape".
	ErrSymlinkEscape = errors.New("apperrors: symlink escapes source tree")

	// ErrRedactUnsupported corresponds to "Oversized + unbounded-regex
	// fail-closed" (SPEC §13, §8.2): a file exceeds --redact-buffer-max
	// with an unbounded custom regex pattern set, so it fails closed rather
	// than being served. Maps to EACCES, event "redact_unsupported".
	ErrRedactUnsupported = errors.New("apperrors: redaction unsupported for this file")

	// ErrNotFound indicates a requested record does not exist. Used by
	// internal/history (SPEC §16) for lookups against the rollup store; it
	// does not appear in the SPEC §13 errno matrix and carries no FUSE
	// errno mapping beyond the fail-closed default (see ToErrno).
	ErrNotFound = errors.New("apperrors: not found")

	// ErrAlreadyExists indicates a record already exists where a unique
	// record was expected. Used by internal/history (SPEC §16). It does not
	// appear in the SPEC §13 errno matrix and carries no FUSE errno mapping
	// beyond the fail-closed default (see ToErrno).
	ErrAlreadyExists = errors.New("apperrors: already exists")
)

// event holds the errno and the SPEC §22 / §10 event name associated with a
// sentinel error.
type event struct {
	errno syscall.Errno
	name  string
}

// mapping is the single source of truth for sentinel → (errno, event name).
// Ordering matters only for readability; lookups are done via errors.Is so
// wrapped errors (fmt.Errorf("...: %w", sentinel)) still match.
var mapping = []struct {
	err error
	event
}{
	{ErrHidden, event{syscall.EACCES, "denied"}},
	{ErrMaskedReadOnly, event{syscall.EACCES, "denied"}},
	{ErrRebuildTimeout, event{syscall.EIO, "rebuild_timeout"}},
	{ErrPanic, event{syscall.EIO, "panic"}},
	// ErrConfigParse: paths fold to HIDDEN (FR-6/FR-13); EACCES matches the
	// fail-closed access outcome, "config_error" is the reporting event name.
	{ErrConfigParse, event{syscall.EACCES, "config_error"}},
	{ErrSymlinkEscape, event{syscall.ENOENT, "symlink_escape"}},
	{ErrRedactUnsupported, event{syscall.EACCES, "redact_unsupported"}},
}

// ToErrno implements SPEC §13's canonical errno mapping. It is the single
// translation point from an apperrors sentinel (matched via errors.Is, so
// errors wrapped with %w still resolve) to a syscall.Errno. Per SPEC §13/§21,
// only internal/mount may call this function.
//
// Unmapped or unknown errors — including nil, which should never reach this
// function but must not be allowed to leak as a false "success" — map to
// syscall.EIO, the fail-closed default (SPEC §20.2: ambiguity resolves to the
// option where the agent behind the mount sees less).
func ToErrno(err error) syscall.Errno {
	for _, m := range mapping {
		if errors.Is(err, m.err) {
			return m.event.errno
		}
	}
	return syscall.EIO
}

// Event returns the SPEC §10/§22 event name associated with a sentinel error
// (e.g. "denied", "rebuild_timeout", "panic", "config_error",
// "symlink_escape", "redact_unsupported"), matched via errors.Is so wrapped
// errors still resolve. Callers use this to emit the correctly named FUSE
// event alongside the errno produced by ToErrno.
//
// Unmapped or unknown errors return "unknown", mirroring ToErrno's fail-closed
// EIO default: an unrecognized error must never be silently omitted from
// observability.
func Event(err error) string {
	for _, m := range mapping {
		if errors.Is(err, m.err) {
			return m.event.name
		}
	}
	return "unknown"
}
