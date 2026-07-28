// Package config is the single source of truth for every JanusFS tunable: one
// Config struct, its defaults (Default), file and env-var overrides (ApplyFile,
// ApplyEnv), and a Validate method that catches conflicts before any FUSE call
// is made. CLI flags are registered and parsed by cmd/janusfs (cobra), binding
// directly to Config fields, so that cobra's flag/help machinery stays in the
// one package that owns the command tree.
//
// No package other than cmd/janusfs reads a flag or os.Getenv directly, and
// every tunable has a field here. That is what lets `janusfs paths` and the
// startup config summary be complete rather than best-effort.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Default tunable values.
const (
	// DefaultUIPort is the default --ui-port.
	DefaultUIPort = 7381

	// DefaultCacheMaxBytes is the default --cache-max-bytes RAM-cache budget
	// in bytes.
	DefaultCacheMaxBytes int64 = 256 * 1024 * 1024

	// DefaultCacheMaxFile is the default --cache-max-file per-entry cap in
	// bytes. A file larger than this is refused from the cache and
	// stream-redacted on every read instead.
	DefaultCacheMaxFile int64 = 64 * 1024 * 1024

	// DefaultHistoryRetentionDays is the default --history-retention window
	// in days.
	DefaultHistoryRetentionDays = 30

	// DefaultRedactBufferMax is the default --redact-buffer-max whole-file
	// buffering cap in bytes, applied when a pattern set contains an
	// unbounded regex that cannot be matched chunk by chunk.
	DefaultRedactBufferMax int64 = 512 * 1024 * 1024
)

// Config holds every JanusFS tunable, plus the positional mount arguments.
// Flag and env parsing populates a Config elsewhere (cmd/janusfs); this package
// only defines the struct, its defaults (Default), and validation (Validate).
type Config struct {
	// Src is the source tree being protected. Positional only: a source path
	// has no meaningful default, so there is deliberately no env equivalent.
	Src string

	// Mountpoint is where the sanitized view appears. Positional only, for
	// the same reason as Src.
	Mountpoint string

	// UIPort is the localhost port the dashboard/API listens on
	// (--ui-port). Env: JANUSFS_UI_PORT.
	UIPort int

	// CacheMaxBytes is the RAM-cache budget in bytes (--cache-max-bytes).
	// Env: JANUSFS_CACHE_MAX_BYTES.
	CacheMaxBytes int64

	// CacheMaxFile is the per-entry cache size cap in bytes; files larger
	// than this are refused from the cache and streamed instead
	// (--cache-max-file). Env: JANUSFS_CACHE_MAX_FILE.
	CacheMaxFile int64

	// HistoryRetentionDays is how many days of history rollups are kept
	// before pruning (--history-retention).
	// Env: JANUSFS_HISTORY_RETENTION_DAYS.
	HistoryRetentionDays int

	// NoHistory disables history persistence entirely when true
	// (--no-history). Env: JANUSFS_NO_HISTORY.
	NoHistory bool

	// RedactBufferMax is the hard cap, in bytes, on whole-file buffering
	// for unbounded custom regexes before failing closed to HIDDEN
	// (--redact-buffer-max). Env: JANUSFS_REDACT_BUFFER_MAX.
	RedactBufferMax int64

	// MountRoot is the directory under which a mountpoint is derived when
	// <mountpoint> is omitted (install --root, env JANUSFS_MOUNT_ROOT). Empty
	// disables derivation, making <mountpoint> required; the default is
	// ~/.janusfs/mounts so first-run mounting does not require install.
	MountRoot string
}

// Default returns a Config populated with every tunable's documented default
// value. Src and Mountpoint are left empty: Src has no meaningful default, and
// Mountpoint is derived later from MountRoot when omitted.
func Default() Config {
	return Config{
		UIPort:               DefaultUIPort,
		CacheMaxBytes:        DefaultCacheMaxBytes,
		CacheMaxFile:         DefaultCacheMaxFile,
		HistoryRetentionDays: DefaultHistoryRetentionDays,
		NoHistory:            false,
		RedactBufferMax:      DefaultRedactBufferMax,
		MountRoot:            DefaultMountRoot(),
	}
}

// ApplyEnv overlays JANUSFS_* environment-variable values onto cfg in place,
// leaving any field whose env var is unset (or empty) untouched. Callers apply
// this to a Default() config before registering CLI flags, so a flag's default
// reflects the env override and an explicit flag still wins if the user passes
// one. The resulting precedence is Default, then file, then env, then flag.
func ApplyEnv(cfg *Config) error {
	if err := envInt("JANUSFS_UI_PORT", &cfg.UIPort); err != nil {
		return err
	}
	if err := envInt64("JANUSFS_CACHE_MAX_BYTES", &cfg.CacheMaxBytes); err != nil {
		return err
	}
	if err := envInt64("JANUSFS_CACHE_MAX_FILE", &cfg.CacheMaxFile); err != nil {
		return err
	}
	if err := envInt("JANUSFS_HISTORY_RETENTION_DAYS", &cfg.HistoryRetentionDays); err != nil {
		return err
	}
	if err := envBool("JANUSFS_NO_HISTORY", &cfg.NoHistory); err != nil {
		return err
	}
	if err := envInt64("JANUSFS_REDACT_BUFFER_MAX", &cfg.RedactBufferMax); err != nil {
		return err
	}
	if s, ok := os.LookupEnv("JANUSFS_MOUNT_ROOT"); ok && s != "" {
		cfg.MountRoot = s
	}
	return nil
}

// DefaultMountRoot is the built-in mount root and the default shown by
// `janusfs install`'s interactive prompt: alongside ~/.janusfs/config (global
// rules) and ~/.janusfs/settings.json, mounts live at
// ~/.janusfs/mounts/<full-src-path>.
func DefaultMountRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".janusfs", "mounts")
}

// SettingsPath is the persistent settings file, ~/.janusfs/settings.json.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "settings.json"), nil
}

type fileSettings struct {
	MountRoot string `json:"mount_root"`
}

// ApplyFile overlays ~/.janusfs/settings.json onto cfg (Default -> File -> Env -> Flag).
// A missing file is not an error.
func ApplyFile(cfg *Config) error {
	path, err := SettingsPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: reading %s: %w", path, err)
	}
	var fsettings fileSettings
	if err := json.Unmarshal(data, &fsettings); err != nil {
		return fmt.Errorf("config: parsing %s: %w", path, err)
	}
	if fsettings.MountRoot != "" {
		cfg.MountRoot = fsettings.MountRoot
	}
	return nil
}

// SaveSettings persists mountRoot to ~/.janusfs/settings.json (0700 dir, 0600 file).
func SaveSettings(mountRoot string) error {
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(fileSettings{MountRoot: mountRoot}, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding settings: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// MountsPath is the registry of mounts started via `janusfs mount`,
// ~/.janusfs/mounts.json — the input to `janusfs resume`.
func MountsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "mounts.json"), nil
}

// MountRecord is one entry in the mounts registry: a src/mountpoint pair the
// daemon can remount on start, plus an optional human label shown in the
// dashboard.
type MountRecord struct {
	Src        string `json:"src"`
	Mountpoint string `json:"mountpoint"`
	Label      string `json:"label,omitempty"`
}

// LoadMounts reads the mounts registry. A missing file is not an error.
func LoadMounts() ([]MountRecord, error) {
	path, err := MountsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var records []MountRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	return records, nil
}

func saveMounts(records []MountRecord) error {
	path, err := MountsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding mounts registry: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// RecordMount upserts (src, mountpoint, label) into the mounts registry, keyed
// by mountpoint, so the daemon can remount it on start with the same label.
func RecordMount(src, mountpoint, label string) error {
	records, err := LoadMounts()
	if err != nil {
		return err
	}
	for i, r := range records {
		if r.Mountpoint == mountpoint {
			records[i].Src = src
			records[i].Label = label
			return saveMounts(records)
		}
	}
	records = append(records, MountRecord{Src: src, Mountpoint: mountpoint, Label: label})
	return saveMounts(records)
}

// RemoveMount drops mountpoint from the mounts registry (called on explicit
// `janusfs umount`, so resume doesn't bring back a mount the user chose to
// stop). Absent entries are a no-op.
func RemoveMount(mountpoint string) error {
	records, err := LoadMounts()
	if err != nil {
		return err
	}
	out := records[:0]
	for _, r := range records {
		if r.Mountpoint != mountpoint {
			out = append(out, r)
		}
	}
	return saveMounts(out)
}

func envInt(key string, dst *int) error {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("config: parsing %s=%q: %w", key, s, err)
	}
	*dst = v
	return nil
}

func envInt64(key string, dst *int64) error {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("config: parsing %s=%q: %w", key, s, err)
	}
	*dst = v
	return nil
}

func envBool(key string, dst *bool) error {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("config: parsing %s=%q: %w", key, s, err)
	}
	*dst = v
	return nil
}

// ResolveMountpoint derives the mountpoint when Mountpoint is empty and
// MountRoot is set, creating the directory (0700) if it doesn't exist yet.
// A no-op if Mountpoint is already set or MountRoot is empty. Call before Validate.
//
// The derived path mirrors the source's full, symlink-resolved absolute path
// under MountRoot: `mount /Users/me/projects/app` with root ~/janusroot mounts
// at ~/janusroot/Users/me/projects/app. Every source maps to a unique,
// predictable location — two sources never collide, and there is deliberately
// no override: a friendly name is a dashboard label, not a different path.
func (c *Config) ResolveMountpoint() error {
	if c.Mountpoint != "" || c.MountRoot == "" {
		return nil
	}
	if c.Src == "" {
		return fmt.Errorf("config: src is required to derive a mountpoint: %w", errEmptyPath)
	}
	srcAbs, err := absClean(c.Src)
	if err != nil {
		return fmt.Errorf("config: resolving src %q: %w", c.Src, err)
	}
	// filepath.Join cleans the leading slash of srcAbs, nesting the full
	// source path under MountRoot rather than anchoring at it.
	derived := filepath.Join(c.MountRoot, srcAbs)
	if err := os.MkdirAll(derived, 0o700); err != nil {
		return fmt.Errorf("config: creating derived mountpoint %q under mount root: %w", derived, err)
	}
	c.Mountpoint = derived
	return nil
}

// Validate checks the mount preconditions: both <src> and <mountpoint> must
// exist, <mountpoint> must be an empty directory, and neither may be a prefix of
// the other (checked using absolute, cleaned paths). Any violation aborts the
// mount attempt before a single FUSE call is made, which is what keeps a
// half-established mount from ever existing.
func (c Config) Validate() error {
	// These are user-facing preconditions surfaced directly by the CLI, so the
	// messages read for an operator — no "config:" package prefix, no
	// redundant wrapped syscall text.
	if c.Src == "" {
		return fmt.Errorf("source path is required: %w", errEmptyPath)
	}
	if c.Mountpoint == "" {
		return fmt.Errorf("mountpoint is required: %w", errEmptyPath)
	}

	srcAbs, err := absClean(c.Src)
	if err != nil {
		return fmt.Errorf("resolving source %q: %w", c.Src, err)
	}
	mountAbs, err := absClean(c.Mountpoint)
	if err != nil {
		return fmt.Errorf("resolving mountpoint %q: %w", c.Mountpoint, err)
	}

	srcInfo, err := os.Stat(srcAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source %q does not exist", c.Src)
		}
		return fmt.Errorf("cannot access source %q: %w", c.Src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source %q is not a directory", c.Src)
	}

	mountInfo, err := os.Stat(mountAbs)
	if err != nil {
		return fmt.Errorf("mountpoint %q is unavailable: %w", c.Mountpoint, err)
	}
	if !mountInfo.IsDir() {
		return fmt.Errorf("mountpoint %q is not a directory", c.Mountpoint)
	}

	empty, err := isEmptyDir(mountAbs)
	if err != nil {
		return fmt.Errorf("checking mountpoint %q: %w", c.Mountpoint, err)
	}
	if !empty {
		return fmt.Errorf("mountpoint %q is not empty: %w", c.Mountpoint, ErrMountpointNotEmpty)
	}

	if pathsOverlap(srcAbs, mountAbs) {
		return fmt.Errorf("source %q and mountpoint %q overlap: %w", c.Src, c.Mountpoint, errOverlap)
	}

	return nil
}

var (
	errEmptyPath = errors.New("config: path must not be empty")
	errOverlap   = errors.New("config: src and mountpoint overlap")

	// ErrMountpointNotEmpty is exported so callers deriving a mountpoint
	// (ResolveMountpoint) can detect a leaf collision with a live mount and
	// add a --name/explicit-mountpoint remedy via errors.Is.
	ErrMountpointNotEmpty = errors.New("config: directory is not empty")
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
	defer func() { _ = f.Close() }()

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
// under the other), or the two are identical. In the disjoint mount model src
// and mountpoint must not overlap at all, or the view would nest inside its own
// backing tree.
func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	aWithSep := a + string(filepath.Separator)
	bWithSep := b + string(filepath.Separator)
	return strings.HasPrefix(bWithSep, aWithSep) || strings.HasPrefix(aWithSep, bWithSep)
}
