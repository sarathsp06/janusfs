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
}
