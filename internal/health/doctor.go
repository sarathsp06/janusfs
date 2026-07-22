// Package health implements FR-29's runtime diagnostics: macFUSE status,
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

// Report is a full FR-29 diagnostic report.
type Report struct {
	MacFUSE  MacFUSEStatus `json:"macfuse"`
	Mounts   []MountInfo   `json:"mounts"`
	Runtime  RuntimeInfo   `json:"runtime"`
	Version  string        `json:"version"`
	Warnings []string      `json:"warnings,omitempty"`
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
}

// RuntimeInfo is Go runtime + OS statistics.
type RuntimeInfo struct {
	GoVersion    string `json:"goVersion"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	NumCPU       int    `json:"numCPU"`
	NumGoroutine int    `json:"numGoroutine"`
}

// Run executes all health checks and returns a combined report.
func Run(pidfileDir string) *Report {
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
						Pidfile:    filepath.Join(pidfileDir, e.Name()),
						Mountpoint: strings.TrimSuffix(e.Name(), ".pid"),
					}
					data, err := os.ReadFile(mi.Pidfile)
					if err != nil {
						continue
					}
					mi.PID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
					mi.Alive = pidAlive(mi.PID)
					if !mi.Alive {
						r.Warnings = append(r.Warnings, fmt.Sprintf("stale pidfile for mountpoint %s (pid %d)", mi.Mountpoint, mi.PID))
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

	return r
}
