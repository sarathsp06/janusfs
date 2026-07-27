// Package health collects runtime diagnostics: macFUSE status,
// active mount discovery via pidfiles, and Go runtime stats. It is the
// single package all diagnostic commands consult.
package health

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// Report is a full diagnostic report.
type Report struct {
	MacFUSE  MacFUSEStatus  `json:"macfuse"`
	Mounts   []MountInfo    `json:"mounts"`
	Watchdog WatchdogStatus `json:"watchdog"`
	Runtime  RuntimeInfo    `json:"runtime"`
	Version  string         `json:"version"`
	Warnings []string       `json:"warnings,omitempty"`
}

// WatchdogStatus reports whether the crash-recovery supervisor (`janusfs
// watchdog`, spawned detached by the daemon at startup) is present and alive.
// A daemon running without one is not an error — mounts work fine — but
// recovery from an ungraceful daemon death becomes manual instead of
// automatic, so this is surfaced as a warning, not silence.
type WatchdogStatus struct {
	// Present is true when a watchdog pidfile was found at all.
	Present bool `json:"present"`
	PID     int  `json:"pid,omitempty"`
	Alive   bool `json:"alive"`
}

// MacFUSEStatus reports whether the macFUSE kext is loaded.
type MacFUSEStatus struct {
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// MountInfo describes one active JanusFS mount (from pidfile).
type MountInfo struct {
	PID        int    `json:"pid"`
	Mountpoint string `json:"mountpoint"`
	Pidfile    string `json:"pidfile"`
	Alive      bool   `json:"alive"`

	// MountpointKnown is true when Mountpoint is a real, absolute path read
	// from the pidfile's own second line. When false, Mountpoint falls back to
	// the pidfile's basename — a SHA-256 hash of the real mountpoint, not a
	// path — because the pidfile predates that field. Callers must not present
	// a false Mountpoint as an actionable path.
	MountpointKnown bool `json:"mountpointKnown"`
}

// RuntimeInfo is Go runtime + OS statistics.
type RuntimeInfo struct {
	GoVersion    string `json:"goVersion"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	NumCPU       int    `json:"numCPU"`
	NumGoroutine int    `json:"numGoroutine"`
}

// Run executes all health checks and returns a combined report. watchdogPidfile
// is the path to the watchdog's own pidfile (~/.janusfs/watchdog.pid) — deliberately
// outside pidfileDir, since pidfileDir is scanned for *.pid files and treated as
// one mount per file; colocating the watchdog's pidfile there would make it show
// up as a phantom mount.
func Run(pidfileDir, watchdogPidfile string) *Report {
	r := &Report{}

	r.MacFUSE = checkMacFUSE()
	r.Runtime = RuntimeInfo{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
	}

	bi, ok := debug.ReadBuildInfo()
	if ok {
		r.Version = bi.Main.Version
		if r.Version == "" {
			r.Version = "(devel)"
		}
	}

	if pidfileDir != "" {
		entries, err := os.ReadDir(pidfileDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".pid") {
					mi := MountInfo{
						Pidfile: filepath.Join(pidfileDir, e.Name()),
						// Fallback: the pidfile basename is a SHA-256 hash of
						// the real mountpoint, not a path. Overwritten below
						// with the real path when the pidfile carries one.
						Mountpoint: strings.TrimSuffix(e.Name(), ".pid"),
					}
					data, err := os.ReadFile(mi.Pidfile)
					if err != nil {
						continue
					}
					firstLine, rest, hasSecondLine := strings.Cut(string(data), "\n")
					mi.PID, _ = strconv.Atoi(strings.TrimSpace(firstLine))
					if hasSecondLine {
						if mp := strings.TrimSpace(rest); mp != "" {
							mi.Mountpoint = mp
							mi.MountpointKnown = true
						}
					}
					mi.Alive = pidAlive(mi.PID)
					if !mi.Alive {
						if mi.MountpointKnown {
							r.Warnings = append(r.Warnings, fmt.Sprintf("stale pidfile for mountpoint %s (pid %d)", mi.Mountpoint, mi.PID))
						} else {
							r.Warnings = append(r.Warnings, fmt.Sprintf("stale pidfile %s (pid %d, mountpoint unknown — predates mountpoint recording)", mi.Pidfile, mi.PID))
						}
					}
					r.Mounts = append(r.Mounts, mi)
				}
			}
		}
	}

	if !r.MacFUSE.Installed {
		if runtime.GOOS == "darwin" {
			r.Warnings = append(r.Warnings, "macFUSE is not installed; mounts require macFUSE (brew install --cask macfuse)")
		} else {
			r.Warnings = append(r.Warnings, "FUSE is not installed or /dev/fuse is missing; mounts require FUSE")
		}
	}

	if watchdogPidfile != "" {
		if data, err := os.ReadFile(watchdogPidfile); err == nil {
			r.Watchdog.Present = true
			r.Watchdog.PID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			r.Watchdog.Alive = pidAlive(r.Watchdog.PID)
			if !r.Watchdog.Alive {
				r.Warnings = append(r.Warnings, fmt.Sprintf("watchdog pidfile found but pid %d is not alive; crash recovery is not active", r.Watchdog.PID))
			}
		} else {
			r.Warnings = append(r.Warnings, "no watchdog is supervising this daemon; an ungraceful daemon death will leave mounts hung until `janusfs umount` is run manually")
		}
	}

	return r
}
