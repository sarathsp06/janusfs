package backing

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func openTestRoot(t *testing.T, dir string) *Root {
	t.Helper()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestOpenAtReadsFileContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	fd, err := r.OpenAt("a.txt", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer func() { _ = unix.Close(fd) }()

	buf := make([]byte, 16)
	n, err := unix.Read(fd, buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("read %q, want %q", buf[:n], "hello")
	}
}

func TestOpenAtRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	r := openTestRoot(t, dir)

	if _, err := r.OpenAt("../etc/passwd", unix.O_RDONLY, 0); err == nil {
		t.Fatal("expected an error opening a path with a traversing component")
	}
}

func TestStatAtFollowsSymlinkLstatDoesNot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	st, err := r.StatAt("link.txt")
	if err != nil {
		t.Fatalf("StatAt: %v", err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		t.Error("StatAt should follow the symlink and report the target's mode, not the link's")
	}

	lst, err := r.LstatAt("link.txt")
	if err != nil {
		t.Fatalf("LstatAt: %v", err)
	}
	if lst.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Error("LstatAt should report the symlink itself, not follow it")
	}
}

func TestReadlinkAt(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/some/target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	target, err := r.ReadlinkAt("link")
	if err != nil {
		t.Fatalf("ReadlinkAt: %v", err)
	}
	if target != "/some/target" {
		t.Errorf("ReadlinkAt = %q, want %q", target, "/some/target")
	}
}

func TestUnlinkAtRemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	if err := r.UnlinkAt("gone.txt", false); err != nil {
		t.Fatalf("UnlinkAt: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed, stat err = %v", err)
	}
}

func TestUnlinkAtRemovesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	if err := r.UnlinkAt("subdir", true); err != nil {
		t.Fatalf("UnlinkAt(dir=true): %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("expected directory to be removed, stat err = %v", err)
	}
}

func TestRenameAt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	if err := r.RenameAt("old.txt", "new.txt"); err != nil {
		t.Fatalf("RenameAt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Errorf("expected new.txt to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("expected old.txt to be gone, err = %v", err)
	}
}

func TestMkdirAt(t *testing.T) {
	dir := t.TempDir()
	r := openTestRoot(t, dir)

	if err := r.MkdirAt("newdir", 0o755); err != nil {
		t.Fatalf("MkdirAt: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "newdir"))
	if err != nil || !info.IsDir() {
		t.Errorf("expected newdir to exist as a directory, err=%v", err)
	}
}

func TestSymlinkAt(t *testing.T) {
	dir := t.TempDir()
	r := openTestRoot(t, dir)

	if err := r.SymlinkAt("/some/target", "mylink"); err != nil {
		t.Fatalf("SymlinkAt: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dir, "mylink"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "/some/target" {
		t.Errorf("symlink target = %q, want %q", target, "/some/target")
	}
}

func TestLinkAtCreatesHardlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orig.txt"), []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	if err := r.LinkAt("orig.txt", "linked.txt"); err != nil {
		t.Fatalf("LinkAt: %v", err)
	}
	origInfo, err := os.Stat(filepath.Join(dir, "orig.txt"))
	if err != nil {
		t.Fatal(err)
	}
	linkedInfo, err := os.Stat(filepath.Join(dir, "linked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(origInfo, linkedInfo) {
		t.Error("expected linked.txt to be a hardlink to the same inode as orig.txt")
	}
}

func TestChmodAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openTestRoot(t, dir)

	if err := r.ChmodAt("f.txt", 0o600); err != nil {
		t.Fatalf("ChmodAt: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after ChmodAt = %o, want 0600", info.Mode().Perm())
	}
}

// TestOperationsAfterRootPathReplacedBySymlink is the core justification for
// this whole package: once a Root is opened, later replacing what its OWN
// path names (e.g. with a symlink, or by something else being mounted there)
// must not affect operations already relative to the retained descriptor —
// they keep resolving against the original directory, not whatever now
// occupies that path string.
func TestOperationsAfterRootPathReplacedBySymlink(t *testing.T) {
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := openTestRoot(t, realDir)

	// Replace what "realDir" names on disk: move it aside and put a symlink
	// to an unrelated decoy directory in its place.
	decoy := filepath.Join(parent, "decoy")
	if err := os.Mkdir(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "f.txt"), []byte("decoy content"), 0o644); err != nil {
		t.Fatal(err)
	}
	movedReal := filepath.Join(parent, "real-moved")
	if err := os.Rename(realDir, movedReal); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, realDir); err != nil {
		t.Fatal(err)
	}

	// r was opened against the ORIGINAL realDir before the swap. Its
	// operations must still see "original", never "decoy content" — proving
	// the descriptor, not the path string, is what's authoritative.
	fd, err := r.OpenAt("f.txt", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenAt after path swap: %v", err)
	}
	defer func() { _ = unix.Close(fd) }()
	buf := make([]byte, 32)
	n, err := unix.Read(fd, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "original" {
		t.Fatalf("expected the retained descriptor to still see the original content, got %q", buf[:n])
	}
}
