// Package backing gives internal/mount descriptor-relative access to a
// source tree's real, on-disk files, closing a time-of-check-to-time-of-use
// window that path-based access cannot: a decision made against one
// resolution of a path, followed by I/O that re-resolves the same path
// string, can be served under a stale decision if a component was swapped
// (e.g. to a symlink) in between. Every operation here is relative to a
// descriptor acquired once, at construction, so it never re-resolves the
// root's own path from a string again.
//
// This package is deliberately scoped to the READ path only for this pass:
// OpenAt, StatAt, LstatAt, and ReadlinkAt are wired into
// internal/mount's read-time decision key (contentKey), the masked/allowed
// read path (readRaw), and the mutation pre-check that stats a not-yet-
// looked-up child (decisionFor). Mutation operations (Unlink, Rename,
// Symlink, Link, Mkdir, Chmod) are implemented here for API completeness and
// their own direct tests, but internal/mount does not yet route through them
// — those still go through the embedded fs.LoopbackNode, as before. Splitting
// "reads" from "mutations" this way is deliberate: replacing the read path
// closes the actual TOCTOU window between a Decision and the bytes served for
// it (the security-relevant one — it's what stands between a policy verdict
// and what a masked or hidden file's real content can leak), while replacing
// every mutation operation and the directory-stream/file-handle plumbing
// LoopbackNode currently provides is a much larger, separate undertaking that
// risks subtly breaking ordinary file operations if rushed.
//
// internal/backing has no dependency on internal/mount or internal/engine,
// so it is directly testable against a temp directory.
package backing

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrInvalidRelPath is returned by every method here when rel is not safe to
// pass to an *at syscall relative to the root descriptor: an absolute path,
// any ".." component, or any empty component. openat and friends resolve a
// relative path component by component starting at dirfd, so a traversing
// relative path escapes the root exactly as effectively as an absolute one
// would — this is a boundary check, not a convenience, and every exported
// method calls it first, with no exceptions.
var ErrInvalidRelPath = errors.New("backing: invalid relative path")

// Root is a handle to a directory that stays valid regardless of what is
// later mounted over its path. Every operation is relative to the retained
// descriptor, so nothing here re-resolves the root's path after construction.
//
// Root is not safe for concurrent Close with any other method; the
// descriptor itself (used read-only via *at syscalls) is safe for concurrent
// use by multiple goroutines, matching a plain file descriptor's usual
// semantics.
type Root struct {
	fd   int
	path string // for error messages and diagnostics only, never for access
}

// Open acquires the descriptor for dir. Must be called BEFORE any mount is
// established over dir — in path-preserving mode, once dir is a mountpoint,
// resolving it by path again would re-enter the mount.
func Open(dir string) (*Root, error) {
	fd, err := unix.Open(dir, rootOpenFlags, 0)
	if err != nil {
		return nil, fmt.Errorf("backing: opening root %q: %w", dir, err)
	}
	return &Root{fd: fd, path: dir}, nil
}

// Close releases the root descriptor. Every *at call made through r after
// Close returns an error; none of them silently re-resolve dir by path as a
// fallback.
func (r *Root) Close() error {
	return unix.Close(r.fd)
}

// validRel reports whether rel is safe to pass to an *at syscall relative to
// r's descriptor. Comparisons are done component-by-component (split on
// "/"), never by substring: a substring check for ".." both misses framings
// like "a/../b" and falsely rejects a legitimate name like "..foo".
func validRel(rel string) error {
	if rel == "" {
		return ErrInvalidRelPath
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "/") {
		return ErrInvalidRelPath
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ErrInvalidRelPath
		}
	}
	return nil
}

// OpenAt opens rel relative to r, with the given open(2) flags and mode.
// Callers choose whether to include O_NOFOLLOW; this function does not
// impose a default, since the correct choice differs by caller — see the
// call sites in internal/mount for the reasoning at each one.
func (r *Root) OpenAt(rel string, flags int, mode uint32) (int, error) {
	if err := validRel(rel); err != nil {
		return -1, err
	}
	fd, err := unix.Openat(r.fd, rel, flags, mode)
	if err != nil {
		return -1, fmt.Errorf("backing: openat %q: %w", rel, err)
	}
	return fd, nil
}

// StatAt stats rel relative to r, following a symlink at rel if present.
func (r *Root) StatAt(rel string) (unix.Stat_t, error) {
	if err := validRel(rel); err != nil {
		return unix.Stat_t{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(r.fd, rel, &st, 0); err != nil {
		return unix.Stat_t{}, fmt.Errorf("backing: fstatat %q: %w", rel, err)
	}
	return st, nil
}

// LstatAt stats rel relative to r, WITHOUT following a symlink at rel: if
// rel itself names a symlink, LstatAt reports the symlink, not its target.
func (r *Root) LstatAt(rel string) (unix.Stat_t, error) {
	if err := validRel(rel); err != nil {
		return unix.Stat_t{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(r.fd, rel, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, fmt.Errorf("backing: fstatat(nofollow) %q: %w", rel, err)
	}
	return st, nil
}

// ReadlinkAt returns the raw target text of the symlink at rel, relative
// to r. Never follows further: a symlink chain is the caller's concern.
func (r *Root) ReadlinkAt(rel string) (string, error) {
	if err := validRel(rel); err != nil {
		return "", err
	}
	// 4096 comfortably covers any realistic symlink target (PATH_MAX on every
	// platform this project supports); growing the buffer on truncation would
	// add complexity for a case that does not occur in practice.
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(r.fd, rel, buf)
	if err != nil {
		return "", fmt.Errorf("backing: readlinkat %q: %w", rel, err)
	}
	return string(buf[:n]), nil
}

// UnlinkAt removes rel relative to r. dir must be true to remove an empty
// directory (equivalent to rmdir), false for any other file type.
func (r *Root) UnlinkAt(rel string, dir bool) error {
	if err := validRel(rel); err != nil {
		return err
	}
	var flags int
	if dir {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(r.fd, rel, flags); err != nil {
		return fmt.Errorf("backing: unlinkat %q: %w", rel, err)
	}
	return nil
}

// RenameAt renames oldRel to newRel, both relative to r (the source root
// never spans two different Root instances in this codebase, so a single
// shared descriptor for both sides is sufficient).
func (r *Root) RenameAt(oldRel, newRel string) error {
	if err := validRel(oldRel); err != nil {
		return err
	}
	if err := validRel(newRel); err != nil {
		return err
	}
	if err := unix.Renameat(r.fd, oldRel, r.fd, newRel); err != nil {
		return fmt.Errorf("backing: renameat %q -> %q: %w", oldRel, newRel, err)
	}
	return nil
}

// MkdirAt creates a directory at rel relative to r.
func (r *Root) MkdirAt(rel string, mode uint32) error {
	if err := validRel(rel); err != nil {
		return err
	}
	if err := unix.Mkdirat(r.fd, rel, mode); err != nil {
		return fmt.Errorf("backing: mkdirat %q: %w", rel, err)
	}
	return nil
}

// SymlinkAt creates a symlink at rel (relative to r) whose content is the
// literal string target. target is not itself validated by validRel: a
// symlink's target is an arbitrary string, not a path resolved relative to r
// (it may be absolute, or relative to the symlink's own directory at
// traversal time — that resolution is the kernel's job when something
// later follows the link, not this call's).
func (r *Root) SymlinkAt(target, rel string) error {
	if err := validRel(rel); err != nil {
		return err
	}
	if err := unix.Symlinkat(target, r.fd, rel); err != nil {
		return fmt.Errorf("backing: symlinkat %q -> %q: %w", rel, target, err)
	}
	return nil
}

// LinkAt creates a new hardlink at newRel pointing to the same inode as
// oldRel, both relative to r. Does not follow a symlink at oldRel (flags=0):
// matches traditional link(2) semantics and fs.LoopbackNode's own behavior —
// linking a name that happens to be a symlink links the symlink itself, not
// whatever it points to.
func (r *Root) LinkAt(oldRel, newRel string) error {
	if err := validRel(oldRel); err != nil {
		return err
	}
	if err := validRel(newRel); err != nil {
		return err
	}
	if err := unix.Linkat(r.fd, oldRel, r.fd, newRel, 0); err != nil {
		return fmt.Errorf("backing: linkat %q -> %q: %w", oldRel, newRel, err)
	}
	return nil
}

// ChmodAt changes the mode of rel relative to r, following a symlink at rel
// if present (flags=0), matching chmod(2)'s traditional behavior (there is
// no meaningful "change the mode of a symlink itself" on most platforms).
func (r *Root) ChmodAt(rel string, mode uint32) error {
	if err := validRel(rel); err != nil {
		return err
	}
	if err := unix.Fchmodat(r.fd, rel, mode, 0); err != nil {
		return fmt.Errorf("backing: fchmodat %q: %w", rel, err)
	}
	return nil
}
