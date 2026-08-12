//go:build darwin

package execrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxAvailable(t *testing.T) {
	if err := sandboxAvailable(); err != nil {
		t.Fatalf("expected /usr/bin/sandbox-exec to be available on darwin, got: %v", err)
	}
}

func TestSandboxAvailableAt(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing path is an error", func(t *testing.T) {
		if err := sandboxAvailableAt(filepath.Join(dir, "nope")); err == nil {
			t.Fatal("expected error for missing path")
		}
	})

	t.Run("directory is not an executable", func(t *testing.T) {
		if err := sandboxAvailableAt(dir); err == nil {
			t.Fatal("expected error when path is a directory")
		}
	})

	t.Run("non-executable file is rejected", func(t *testing.T) {
		f := filepath.Join(dir, "not-exec")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := sandboxAvailableAt(f); err == nil {
			t.Fatal("expected error for non-executable file")
		}
	})

	t.Run("executable file passes", func(t *testing.T) {
		f := filepath.Join(dir, "ok")
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := sandboxAvailableAt(f); err != nil {
			t.Fatalf("expected success for executable file, got %v", err)
		}
	})
}

func TestCanonicalizeWithFirmlinkTwin(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	// A path that traverses a symlink must resolve to the same canonical
	// target as the real path — this is the /var vs /private/var case that
	// caused the first spike attempt to silently allow everything.
	targets, err := canonicalizeWithFirmlinkTwin(linkDir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if targets[0] != resolvedReal {
		t.Fatalf("expected canonical form %q, got %q", resolvedReal, targets[0])
	}

	// A path under a firmlink root must also produce the
	// /System/Volumes/Data twin, so a deny is not bypassable by using the
	// un-denied form.
	twinTargets, err := canonicalizeWithFirmlinkTwin("/Users")
	if err != nil {
		t.Fatal(err)
	}
	if len(twinTargets) != 2 {
		t.Fatalf("expected canonical + firmlink twin for /Users, got %v", twinTargets)
	}
	if !strings.HasPrefix(twinTargets[1], "/System/Volumes/Data") {
		t.Fatalf("expected firmlink twin under /System/Volumes/Data, got %v", twinTargets)
	}
}

func TestCanonicalReadOnlyDenyTargets(t *testing.T) {
	t.Run("empty home returns nil, not an error", func(t *testing.T) {
		targets, err := canonicalReadOnlyDenyTargets("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if targets != nil {
			t.Fatalf("expected nil targets, got %v", targets)
		}
	})

	t.Run("missing ~/.janusfs returns nil, not an error", func(t *testing.T) {
		home := t.TempDir() // fresh dir, guaranteed no .janusfs inside
		targets, err := canonicalReadOnlyDenyTargets(home)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if targets != nil {
			t.Fatalf("expected nil targets for missing ~/.janusfs, got %v", targets)
		}
	})

	t.Run("present ~/.janusfs is denied", func(t *testing.T) {
		home := t.TempDir()
		janusDir := filepath.Join(home, ".janusfs")
		if err := os.Mkdir(janusDir, 0o700); err != nil {
			t.Fatal(err)
		}
		targets, err := canonicalReadOnlyDenyTargets(home)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(targets) == 0 {
			t.Fatalf("expected at least one deny target for existing ~/.janusfs")
		}
		resolved, err := filepath.EvalSymlinks(janusDir)
		if err != nil {
			t.Fatal(err)
		}
		if targets[0] != resolved {
			t.Fatalf("expected %q, got %q", resolved, targets[0])
		}
	})
}

func TestSandboxProfile(t *testing.T) {
	mnt := []string{"/tmp/mount"}

	t.Run("empty read-write deny set is an error", func(t *testing.T) {
		if _, err := sandboxProfile(nil, nil, mnt); err == nil {
			t.Fatal("expected error for empty deny-read-write set")
		}
	})

	t.Run("empty mustAllow set is an error", func(t *testing.T) {
		if _, err := sandboxProfile([]string{"/tmp/src"}, nil, nil); err == nil {
			t.Fatal("expected error for empty mustAllow set")
		}
	})

	t.Run("ordering: allow default, then deny, then the mountpoint re-allow last", func(t *testing.T) {
		profile, err := sandboxProfile([]string{"/tmp/src"}, nil, mnt)
		if err != nil {
			t.Fatal(err)
		}
		allowDefaultIdx := strings.Index(profile, "(allow default)")
		denyReadIdx := strings.Index(profile, "(deny file-read*")
		denyWriteIdx := strings.Index(profile, "(deny file-write*")
		allowMountIdx := strings.LastIndex(profile, "(allow file-read*")
		if allowDefaultIdx == -1 || denyReadIdx == -1 || denyWriteIdx == -1 || allowMountIdx == -1 {
			t.Fatalf("profile missing expected clauses:\n%s", profile)
		}
		// Seatbelt is last-match-wins: allow default must come first, and the
		// mountpoint re-allow must come after every deny, so it always wins
		// even if a future deny rule happens to cover the mountpoint too.
		if !(allowDefaultIdx < denyReadIdx && denyReadIdx < allowMountIdx && denyWriteIdx < allowMountIdx) {
			t.Fatalf("expected allow default < deny rules < mountpoint re-allow, got:\n%s", profile)
		}
	})

	t.Run("both read and write denied for the source", func(t *testing.T) {
		profile, err := sandboxProfile([]string{"/tmp/src"}, nil, mnt)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(profile, `(deny file-read* (subpath "/tmp/src"))`) {
			t.Fatalf("missing read deny for source:\n%s", profile)
		}
		if !strings.Contains(profile, `(deny file-write* (subpath "/tmp/src"))`) {
			t.Fatalf("missing write deny for source:\n%s", profile)
		}
	})

	t.Run("both read and write re-allowed for the mountpoint", func(t *testing.T) {
		profile, err := sandboxProfile([]string{"/tmp/src"}, nil, mnt)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(profile, `(allow file-read* (subpath "/tmp/mount"))`) {
			t.Fatalf("missing read re-allow for mountpoint:\n%s", profile)
		}
		if !strings.Contains(profile, `(allow file-write* (subpath "/tmp/mount"))`) {
			t.Fatalf("missing write re-allow for mountpoint:\n%s", profile)
		}
	})

	t.Run("mountpoint re-allow wins even when it collides with the read-only deny set", func(t *testing.T) {
		// Regression: the default mount root is ~/.janusfs/mounts/..., so a
		// naive "deny ~/.janusfs, allow everything else" profile denies the
		// mountpoint itself whenever the user hasn't customized --root. The
		// mountpoint re-allow must be positioned so it wins regardless.
		home := "/Users/x/.janusfs"
		mountUnderHome := []string{home + "/mounts/some/project"}
		profile, err := sandboxProfile([]string{"/tmp/src"}, []string{home}, mountUnderHome)
		if err != nil {
			t.Fatal(err)
		}
		roIdx := strings.Index(profile, `(deny file-read* (subpath "`+home+`")`)
		allowMountIdx := strings.Index(profile, `(allow file-read* (subpath "`+home+`/mounts/some/project")`)
		if roIdx == -1 || allowMountIdx == -1 {
			t.Fatalf("profile missing expected clauses:\n%s", profile)
		}
		if allowMountIdx < roIdx {
			t.Fatalf("mountpoint re-allow must come after the colliding deny, got:\n%s", profile)
		}
	})

	t.Run("read-only deny set is read-only, not read+write", func(t *testing.T) {
		profile, err := sandboxProfile([]string{"/tmp/src"}, []string{"/Users/x/.janusfs"}, mnt)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(profile, `"/Users/x/.janusfs"`) != 1 {
			t.Fatalf("expected exactly one deny clause (read-only) for ~/.janusfs, got:\n%s", profile)
		}
		if !strings.Contains(profile, `(deny file-read* (subpath "/Users/x/.janusfs"))`) {
			t.Fatalf("expected read deny for ~/.janusfs:\n%s", profile)
		}
	})

	t.Run("a quote in a deny path is rejected, not silently dropped", func(t *testing.T) {
		if _, err := sandboxProfile([]string{`/tmp/"; (allow default) ;"`}, nil, mnt); err == nil {
			t.Fatal("expected error for a deny path containing a quote")
		}
	})

	t.Run("a newline in a deny path is rejected", func(t *testing.T) {
		if _, err := sandboxProfile([]string{"/tmp/src\n(allow default)"}, nil, mnt); err == nil {
			t.Fatal("expected error for a deny path containing a newline")
		}
	})

	t.Run("a quote in the mustAllow path is rejected", func(t *testing.T) {
		if _, err := sandboxProfile([]string{"/tmp/src"}, nil, []string{`/tmp/"; (deny default) ;"`}); err == nil {
			t.Fatal("expected error for a mustAllow path containing a quote")
		}
	})

	t.Run("a quote in the read-only deny path is rejected", func(t *testing.T) {
		if _, err := sandboxProfile([]string{"/tmp/src"}, []string{`/tmp/"; (allow default) ;"`}, mnt); err == nil {
			t.Fatal("expected error for a read-only deny path containing a quote")
		}
	})

	t.Run("multiple deny paths all appear in the profile", func(t *testing.T) {
		profile, err := sandboxProfile([]string{"/tmp/a", "/tmp/b"}, nil, mnt)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(profile, `(subpath "/tmp/a")`) || !strings.Contains(profile, `(subpath "/tmp/b")`) {
			t.Fatalf("expected both deny paths in profile:\n%s", profile)
		}
	})
}

func TestCanonicalDenyTargets(t *testing.T) {
	// Thin wrapper, but the deny set is load-bearing — if it ever gains
	// logic (e.g. a per-caller allowlist), this test catches an accidental
	// divergence from canonicalizeWithFirmlinkTwin.
	targets, err := canonicalDenyTargets("/Users")
	if err != nil {
		t.Fatal(err)
	}
	twin, err := canonicalizeWithFirmlinkTwin("/Users")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != len(twin) || targets[0] != twin[0] {
		t.Fatalf("canonicalDenyTargets diverged from canonicalizeWithFirmlinkTwin: %v vs %v", targets, twin)
	}
}

func TestAssertMountNotUnderSrc(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(dir, "mount")
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(src, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("disjoint sibling is allowed", func(t *testing.T) {
		if err := assertMountNotUnderSrc(src, sibling); err != nil {
			t.Fatalf("expected sibling mount to be allowed, got %v", err)
		}
	})

	t.Run("mount identical to src is rejected", func(t *testing.T) {
		if err := assertMountNotUnderSrc(src, src); err == nil {
			t.Fatal("expected error when mountpoint equals source")
		}
	})

	t.Run("mount nested under src is rejected", func(t *testing.T) {
		if err := assertMountNotUnderSrc(src, nested); err == nil {
			t.Fatal("expected error when mountpoint is under source")
		}
	})

	t.Run("mount reached via symlink to a nested path is rejected", func(t *testing.T) {
		// EvalSymlinks must run on both sides — a caller passing a symlink
		// that resolves under src still means the mount is under src.
		linkToNested := filepath.Join(dir, "link-to-nested")
		if err := os.Symlink(nested, linkToNested); err != nil {
			t.Fatal(err)
		}
		if err := assertMountNotUnderSrc(src, linkToNested); err == nil {
			t.Fatal("expected error when a symlinked mountpoint resolves under source")
		}
	})

	t.Run("prefix-only similar name (not a parent dir) is allowed", func(t *testing.T) {
		// Guards against a naive strings.HasPrefix without the separator.
		lookalike := filepath.Join(dir, "srcbutnot")
		if err := os.Mkdir(lookalike, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := assertMountNotUnderSrc(src, lookalike); err != nil {
			t.Fatalf("expected sibling with shared prefix to be allowed, got %v", err)
		}
	})

	t.Run("nonexistent src is an error, not a false pass", func(t *testing.T) {
		if err := assertMountNotUnderSrc(filepath.Join(dir, "nope"), sibling); err == nil {
			t.Fatal("expected error resolving nonexistent src")
		}
	})

	t.Run("nonexistent mountpoint is an error", func(t *testing.T) {
		if err := assertMountNotUnderSrc(src, filepath.Join(dir, "nope-mount")); err == nil {
			t.Fatal("expected error resolving nonexistent mountpoint")
		}
	})
}

func TestCanonicalizeWithFirmlinkTwinErrors(t *testing.T) {
	if _, err := canonicalizeWithFirmlinkTwin(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error resolving a nonexistent path")
	}
}

func TestCanonicalizeWithFirmlinkTwinDataVolumePath(t *testing.T) {
	// A path already expressed under /System/Volumes/Data must produce the
	// stripped twin (so a deny rule applied to the "short" form still
	// blocks callers who reach it via the data-volume form).
	if _, err := os.Stat("/System/Volumes/Data/Users"); err != nil {
		t.Skip("/System/Volumes/Data not present; not a firmlinked system")
	}
	targets, err := canonicalizeWithFirmlinkTwin("/System/Volumes/Data/Users")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tgt := range targets {
		if tgt == "/Users" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected /Users twin for /System/Volumes/Data/Users, got %v", targets)
	}
}
