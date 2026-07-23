// Package vfsmeta implements SPEC.md §3.7's .janusfs virtual files:
// conflicts.json and status.json — reflecting live engine/check state
// (FR-28/FR-31). Content is served through the API (and eventually through
// the mount as synthetic inodes).
package vfsmeta

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/sarathsp06/janusfs/internal/check"
)

// Status contains the live mount status fields for status.json.
type Status struct {
	Uptime       string      `json:"uptime"`
	Generation   uint64      `json:"generation"`
	WatcherAlive bool        `json:"watcherAlive"`
	Cache        CacheStatus `json:"cache"`
	GoVersion    string      `json:"goVersion"`
	Version      string      `json:"version"`
	StartTime    time.Time   `json:"startTime"`
}

// CacheStatus describes the provider cache state.
type CacheStatus struct {
	CurrentBytes int64  `json:"currentBytes"`
	MaxBytes     int64  `json:"maxBytes"`
	EntryCount   int    `json:"entryCount"`
	Hits         uint64 `json:"hits"`
	Misses       uint64 `json:"misses"`
	Rebuilds     uint64 `json:"rebuilds"`
}

// ConflictsJSON runs the check linter and returns conflicts.json content.
func ConflictsJSON(root string) ([]byte, error) {
	report, err := check.Run(root)
	if err != nil {
		return nil, fmt.Errorf("vfsmeta: conflicts: %w", err)
	}
	return json.MarshalIndent(report, "", "  ")
}

// StatusJSON builds the status.json content from live state.
func StatusJSON(startTime time.Time, gen uint64, watcherAlive bool, cacheEntries int, cacheBytes int64, cacheHits, cacheMisses, cacheRebuilds uint64) []byte {
	cache := CacheStatus{
		CurrentBytes: cacheBytes,
		EntryCount:   cacheEntries,
		Hits:         cacheHits,
		Misses:       cacheMisses,
		Rebuilds:     cacheRebuilds,
	}

	bi, ok := debug.ReadBuildInfo()
	goVer := runtime.Version()
	if ok {
		goVer = bi.GoVersion
	}

	status := Status{
		Uptime:       time.Since(startTime).Round(time.Second).String(),
		Generation:   gen,
		WatcherAlive: watcherAlive,
		Cache:        cache,
		GoVersion:    goVer,
		Version:      "dev",
		StartTime:    startTime,
	}

	b, _ := json.MarshalIndent(status, "", "  ")
	return b
}
