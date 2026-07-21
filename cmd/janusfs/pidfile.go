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

// pidfilePath implements FR-3's "pidfile at ~/.janusfs/run/<hash-of-mountpoint>.pid":
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
// the ~/.janusfs/run directory (mode 0700, per SPEC §14's ~/.janusfs perms
// invariant) if needed.
func writePidfile(mountpoint string) error {
	path, err := pidfilePath(mountpoint)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("pidfile: creating run directory: %w", err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// readPidfile returns the PID recorded for mountpoint, or 0 if no pidfile
// exists (not itself an error — FR-3's "signals the owning process if
// discoverable" implies "if not discoverable, proceed with unmount anyway").
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
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("pidfile: parsing %q: %w", path, err)
	}
	return pid, nil
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
