// Package config is the single source of truth for every JanusFS tunable
// (SPEC §15): one Config struct, its defaults (Default), env-var overrides
// (ApplyEnv), and a Validate method that catches conflicts (SPEC §15, FR-1)
// before any FUSE call is made. CLI flags are registered and parsed by
// cmd/janusfs (cobra), binding directly to Config fields — see that
// package's doc comment and docs/SPEC_AMENDMENTS.md (2026-07-17) for why
// flag parsing isn't owned here.
//
// Per AGENTS.md / SPEC §21, no package other than cmd/janusfs reads a flag or
// os.Getenv directly; every tunable named anywhere in SPEC.md must have a
// field here. Env vars are read via caarlos0/env's struct tags rather than
// hand-written os.Getenv calls, per docs/SPEC_AMENDMENTS.md (2026-07-16) and
// SPEC §20.4's amended dependency allowlist — this keeps each tunable's
// env binding declared once, next to the field, instead of duplicated in a
// separate parsing function that must be kept in sync by hand.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Default tunable values, per SPEC §11 (--ui-port), §3.8/NFR-4 (cache and
// redact-buffer sizing), and §3.8/FR-45 (history retention).
const (
	// DefaultUIPort is the default --ui-port (SPEC §11: "<uiPort> (default
	// 7381, --ui-port)").
	DefaultUIPort = 7381

	// DefaultCacheMaxBytes is the default --cache-max-bytes RAM-cache budget
	// in bytes (SPEC NFR-4: "RAM-cache budget default 256 MB").
	DefaultCacheMaxBytes int64 = 256 * 1024 * 1024

	// DefaultCacheMaxFile is the default --cache-max-file per-entry cap in
	// bytes (SPEC NFR-4: "single cached file > 64 MB ... is refused").
	DefaultCacheMaxFile int64 = 64 * 1024 * 1024

	// DefaultHistoryRetentionDays is the default --history-retention window
	// in days (SPEC FR-45: "Retention: 30 days default").
	DefaultHistoryRetentionDays = 30

	// DefaultRedactBufferMax is the default --redact-buffer-max whole-file
	// buffering cap in bytes for unbounded custom regexes (SPEC §8.2:
	// "--redact-buffer-max, default 512 MB").
	DefaultRedactBufferMax int64 = 512 * 1024 * 1024
)

// Config holds every tunable named in SPEC.md, plus the positional mount
// arguments required by FR-1. Flag/env parsing populates a Config elsewhere
// (cmd/janusfs); this package only defines the struct, its defaults
// (Default), and validation (Validate).
type Config struct {
	// Src is the source tree being protected (SPEC §2 "Source tree";
	// FR-1's required positional <src>). Positional only: not read from
	// the environment (SPEC FR-1's positionals have no env equivalent).
	Src string `env:"-"`

	// Mountpoint is where the sanitized view appears (SPEC §2 "Mount
	// point"; FR-1's required positional <mountpoint>). Positional only.
	Mountpoint string `env:"-"`

	// UIPort is the localhost port the dashboard/API listens on
	// (--ui-port, SPEC §11).
	UIPort int `env:"JANUSFS_UI_PORT"`

	// CacheMaxBytes is the RAM-cache budget in bytes (--cache-max-bytes,
	// SPEC NFR-4).
	CacheMaxBytes int64 `env:"JANUSFS_CACHE_MAX_BYTES"`

	// CacheMaxFile is the per-entry cache size cap in bytes; files larger
	// than this are refused from the cache and streamed instead
	// (--cache-max-file, SPEC NFR-4).
	CacheMaxFile int64 `env:"JANUSFS_CACHE_MAX_FILE"`

	// HistoryRetentionDays is how many days of history rollups are kept
	// before pruning (--history-retention, SPEC FR-45).
	HistoryRetentionDays int `env:"JANUSFS_HISTORY_RETENTION_DAYS"`

	// NoHistory disables history persistence entirely when true
	// (--no-history, SPEC FR-45).
	NoHistory bool `env:"JANUSFS_NO_HISTORY"`

	// RedactBufferMax is the hard cap, in bytes, on whole-file buffering
	// for unbounded custom regexes before failing closed to HIDDEN
	// (--redact-buffer-max, SPEC §8.2).
	RedactBufferMax int64 `env:"JANUSFS_REDACT_BUFFER_MAX"`
}

// Default returns a Config populated with every tunable's documented default
// value (SPEC §11, NFR-4, FR-45, §8.2). Src and Mountpoint are left empty:
// FR-1's positional arguments have no meaningful default and must always be
// supplied by the caller before Validate is run.
func Default() Config {
	return Config{
		UIPort:               DefaultUIPort,
		CacheMaxBytes:        DefaultCacheMaxBytes,
		CacheMaxFile:         DefaultCacheMaxFile,
		HistoryRetentionDays: DefaultHistoryRetentionDays,
		NoHistory:            false,
		RedactBufferMax:      DefaultRedactBufferMax,
	}
}

// ApplyEnv overlays environment-variable values (via caarlos0/env, using
// each field's `env` tag) onto cfg in place, leaving any field whose env
// var is unset untouched. Callers apply this to a Default() config before
// registering CLI flags, so a flag's default reflects the env override and
// an explicit flag still wins if the user passes one — the ordering SPEC
// §15 requires ("CLI flags ... primary ... env vars are a secondary
// override").
func ApplyEnv(cfg *Config) error {
	if err := env.Parse(cfg); err != nil {
		return fmt.Errorf("config: parsing environment: %w", err)
	}
	return nil
}

// Validate implements FR-1: both <src> and <mountpoint> must exist,
// <mountpoint> must be an empty directory, and neither may be a prefix of
// the other (checked using absolute, cleaned paths). Any violation must
// cause the mount attempt to abort before any FUSE call is made (SPEC §15
// step 1), per the fail-closed tiebreak in SPEC §20.2.
func (c Config) Validate() error {
	if c.Src == "" {
		return fmt.Errorf("config: src is required: %w", errEmptyPath)
	}
	if c.Mountpoint == "" {
		return fmt.Errorf("config: mountpoint is required: %w", errEmptyPath)
	}

	srcAbs, err := absClean(c.Src)
	if err != nil {
		return fmt.Errorf("config: resolving src %q: %w", c.Src, err)
	}
	mountAbs, err := absClean(c.Mountpoint)
	if err != nil {
		return fmt.Errorf("config: resolving mountpoint %q: %w", c.Mountpoint, err)
	}

	srcInfo, err := os.Stat(srcAbs)
	if err != nil {
		return fmt.Errorf("config: src %q must exist: %w", c.Src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("config: src %q must be a directory", c.Src)
	}

	mountInfo, err := os.Stat(mountAbs)
	if err != nil {
		return fmt.Errorf("config: mountpoint %q must exist: %w", c.Mountpoint, err)
	}
	if !mountInfo.IsDir() {
		return fmt.Errorf("config: mountpoint %q must be a directory", c.Mountpoint)
	}

	empty, err := isEmptyDir(mountAbs)
	if err != nil {
		return fmt.Errorf("config: checking mountpoint %q: %w", c.Mountpoint, err)
	}
	if !empty {
		return fmt.Errorf("config: mountpoint %q must be an empty directory: %w", c.Mountpoint, errNotEmpty)
	}

	if pathsOverlap(srcAbs, mountAbs) {
		return fmt.Errorf("config: src %q and mountpoint %q must not overlap: %w", c.Src, c.Mountpoint, errOverlap)
	}

	return nil
}

var (
	errEmptyPath = errors.New("config: path must not be empty")
	errNotEmpty  = errors.New("config: directory is not empty")
	errOverlap   = errors.New("config: src and mountpoint overlap")
)

// absClean resolves p to an absolute, cleaned, symlink-evaluated path so
// overlap and existence checks operate on canonical paths rather than being
// foolable by relative segments, trailing slashes, or symlinks.
func absClean(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// EvalSymlinks requires the path to exist; existence is checked
	// separately by the caller via os.Stat, so fall back to the cleaned
	// absolute path if symlink evaluation fails (e.g. path does not exist
	// yet) rather than masking the real error here.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

// isEmptyDir reports whether dir contains zero entries.
func isEmptyDir(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// pathsOverlap reports whether either of a or b (absolute, cleaned paths) is
// a prefix of the other in the directory sense (i.e. one path lies on or
// under the other), or the two are identical. FR-1 requires src and
// mountpoint to be entirely disjoint.
func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	aWithSep := a + string(filepath.Separator)
	bWithSep := b + string(filepath.Separator)
	return strings.HasPrefix(bWithSep, aWithSep) || strings.HasPrefix(aWithSep, bWithSep)
}
