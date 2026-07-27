package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// pidfilePath returns ~/.janusfs/run/<sha256-of-mountpoint>.pid:
// a stable, filesystem-safe name derived from the mountpoint's absolute path,
// so umount can find the owning process without the caller having to
// remember or pass one.
func pidfilePath(mountpoint string) (string, error) {
	abs, err := filepath.Abs(mountpoint)
	if err != nil {
		return "", fmt.Errorf("pidfile: resolving mountpoint %q: %w", mountpoint, err)
	}
	sum := sha256.Sum256([]byte(abs))
	hash := hex.EncodeToString(sum[:])

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pidfile: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "run", hash+".pid"), nil
}

// writePidfile records the current process's PID for mountpoint, creating
// the ~/.janusfs/run directory if needed. Mode 0700, like everything under
// ~/.janusfs.
//
// The file carries the mountpoint itself as a second line — pidfilePath's own
// name is a one-way SHA-256 hash, so without this, nothing could recover which
// mountpoint a pidfile belongs to (the exact problem that made `janusfs
// doctor` unable to report a real path for a stale pidfile). readPidfile below
// only ever parses the first line, so a pidfile written by an older build
// (PID only, no second line) still reads correctly.
func writePidfile(mountpoint string) error {
	path, err := pidfilePath(mountpoint)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("pidfile: creating run directory: %w", err)
	}
	abs, err := filepath.Abs(mountpoint)
	if err != nil {
		abs = mountpoint
	}
	content := strconv.Itoa(os.Getpid()) + "\n" + abs + "\n"
	return os.WriteFile(path, []byte(content), 0o600)
}

// readPidfile returns the PID recorded for mountpoint, or 0 if no pidfile
// exists. A missing pidfile is not an error: the owning process is signalled
// only if it is discoverable, and otherwise the unmount proceeds anyway.
//
// Only the first line is parsed, so this reads correctly whether or not the
// file carries the mountpoint as a second line (see writePidfile and
// readPidfileMountpoint).
func readPidfile(mountpoint string) (int, error) {
	path, err := pidfilePath(mountpoint)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("pidfile: reading %q: %w", path, err)
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(firstLine))
	if err != nil {
		return 0, fmt.Errorf("pidfile: parsing %q: %w", path, err)
	}
	return pid, nil
}

// readPidfileMountpoint reads the real mountpoint recorded on a pidfile's
// second line, given the pidfile's own path (as found by, e.g., scanning
// ~/.janusfs/run). Returns "" (not an error) if the file predates this field —
// an older single-line pidfile — so callers can fall back to reporting the
// filename hash as unknown rather than presenting it as a path.
func readPidfileMountpoint(pidfilePath string) string {
	data, err := os.ReadFile(pidfilePath)
	if err != nil {
		return ""
	}
	_, rest, found := strings.Cut(string(data), "\n")
	if !found {
		return ""
	}
	return strings.TrimSpace(rest)
}

// pruneMirrorDirs removes the now-empty mountpoint and its empty parent
// directories, stopping before mountRoot. Best-effort; the strict-prefix guard
// means it only ever touches paths under mountRoot.
func pruneMirrorDirs(mountpoint, mountRoot string) {
	if mountRoot == "" {
		return
	}
	mp, err1 := filepath.Abs(mountpoint)
	root, err2 := filepath.Abs(mountRoot)
	if err1 != nil || err2 != nil {
		return
	}
	for mp != root && strings.HasPrefix(mp, root+string(filepath.Separator)) {
		if err := os.Remove(mp); err != nil {
			return
		}
		mp = filepath.Dir(mp)
	}
}

// pidAlive reports whether pid names a live process, using the POSIX
// "signal 0" existence probe: no signal is actually delivered, only the
// existence/permission check is performed. EPERM (process exists but is
// owned by another user) still counts as alive — a real collision either
// way, not something to mount over.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// removePidfile deletes the pidfile for mountpoint, if any. Never treated as
// fatal by callers: a missing pidfile at cleanup time is not an error.
func removePidfile(mountpoint string) error {
	path, err := pidfilePath(mountpoint)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("pidfile: removing %q: %w", path, err)
	}
	return nil
}
