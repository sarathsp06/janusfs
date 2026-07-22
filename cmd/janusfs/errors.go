package main

import "errors"

// This file is the single home for CLI error sentinels and the recurring
// user-facing guidance that would otherwise drift across commands. It is
// deliberately NOT a catalog of every message: per-call-site errors keep
// their context (and their `fmt.Errorf(... %w ...)` wrapping) where they are,
// the idiomatic Go way. Only text that (a) is matched with errors.Is by more
// than one caller, or (b) is guidance repeated in more than one place, earns
// a spot here.

// errDaemonNotRunning is returned by daemonCall when no daemon is listening on
// the control socket, so commands can react (mount errors out with a start
// hint; umount falls back to a direct OS unmount).
var errDaemonNotRunning = errors.New("no janusfs daemon is running")

const (
	// hintStartDaemon is shown when a command needs the daemon but none is up.
	hintStartDaemon = "no janusfs daemon is running; start it first with: janusfs daemon"

	// hintNoMountRoot is shown when a mount is requested but no mount root is
	// configured to derive the mountpoint under.
	hintNoMountRoot = "no mount root configured: run `janusfs install` (or set --mount-root / JANUSFS_MOUNT_ROOT)"
)
