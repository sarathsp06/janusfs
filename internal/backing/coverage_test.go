package backing

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Every exported method must call validRel first. A caller passing "../etc"
// or "" should get ErrInvalidRelPath, never reach a syscall — otherwise a bug
// in the mount adapter could smuggle an escape past the boundary check.
func TestEveryMethodRejectsInvalidRel(t *testing.T) {
	dir := t.TempDir()
	r := openTestRoot(t, dir)

	bad := []string{"..", "/abs", "", "a/../b", "a//b"}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: expected error for invalid rel", name)
			return
		}
		if !errors.Is(err, ErrInvalidRelPath) {
			t.Errorf("%s: err = %v, want ErrInvalidRelPath", name, err)
		}
	}
	for _, rel := range bad {
		_, err := r.OpenAt(rel, unix.O_RDONLY, 0)
		check("OpenAt "+rel, err)
		_, err = r.StatAt(rel)
		check("StatAt "+rel, err)
		_, err = r.LstatAt(rel)
		check("LstatAt "+rel, err)
		_, err = r.ReadlinkAt(rel)
		check("ReadlinkAt "+rel, err)
		check("UnlinkAt "+rel, r.UnlinkAt(rel, false))
		check("MkdirAt "+rel, r.MkdirAt(rel, 0o755))
		check("SymlinkAt "+rel, r.SymlinkAt("target", rel))
		check("ChmodAt "+rel, r.ChmodAt(rel, 0o600))
		check("RenameAt src "+rel, r.RenameAt(rel, "ok"))
		check("RenameAt dst "+rel, r.RenameAt("ok", rel))
		check("LinkAt src "+rel, r.LinkAt(rel, "ok"))
		check("LinkAt dst "+rel, r.LinkAt("ok", rel))
	}
}

// After Close, no *at call may silently re-resolve the path as a fallback:
// every operation must fail with a real syscall error, not succeed.
func TestOperationsFailAfterClose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenAt("f.txt", unix.O_RDONLY, 0); err == nil {
		t.Error("OpenAt after Close: expected error, got nil")
	}
	if _, err := r.StatAt("f.txt"); err == nil {
		t.Error("StatAt after Close: expected error, got nil")
	}
}

// OpenAt with O_NOFOLLOW returns ELOOP when the final component is a
// symlink — this is the actual mechanism protecting the read path from a
// symlink swapped in between decision and I/O.
func TestOpenAtNoFollowRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	fd, err := r.OpenAt("link.txt", unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err == nil {
		_ = unix.Close(fd)
		t.Fatal("expected OpenAt with O_NOFOLLOW to reject a final-component symlink")
	}
}

func TestOpenMissingRootErrors(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error opening a nonexistent root")
	}
}

func TestMkdirAtRejectsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)
	if err := r.MkdirAt("d", 0o755); err == nil {
		t.Error("MkdirAt over an existing directory: expected error")
	}
}

func TestUnlinkAtNonEmptyDirFails(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)
	if err := r.UnlinkAt("sub", true); err == nil {
		t.Error("UnlinkAt(dir=true) on non-empty dir: expected error")
	}
}
