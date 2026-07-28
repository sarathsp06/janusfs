package main

import "github.com/sarathsp06/janusfs/internal/control"

// This file is the single home for CLI error sentinels and the recurring
// user-facing guidance that would otherwise drift across commands. It is
// deliberately NOT a catalog of every message: per-call-site errors keep
// their context (and their `fmt.Errorf(... %w ...)` wrapping) where they are,
// the idiomatic Go way. Only text that (a) is matched with errors.Is by more
// than one caller, or (b) is guidance repeated in more than one place, earns
// a spot here.

// errDaemonNotRunning is the local short name for the control-socket sentinel
// returned when no daemon is listening, so commands can react (mount errors
// out with a start hint; umount falls back to a direct OS unmount). It is the
// same value as internal/control.ErrDaemonNotRunning — aliased, not
// re-declared, so the two can never drift out of sync.
var errDaemonNotRunning = control.ErrDaemonNotRunning

const (
	// hintStartDaemon is shown when a command needs the daemon but none is up.
	hintStartDaemon = "no janusfs daemon is running; start one first:\n" +
		"  foreground (dev):  janusfs daemon\n" +
		"  background:        janusfs daemon --background"

	// hintNoMountRoot is shown when a mount is requested but no mount root is
	// configured to derive the mountpoint under.
	hintNoMountRoot = "no mount root configured: run `janusfs install --root <dir>` or set JANUSFS_MOUNT_ROOT"
)
