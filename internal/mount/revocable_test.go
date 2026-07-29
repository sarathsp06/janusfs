//go:build fuseintegration && (darwin || linux)

package mount

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestReloadRevokesOpenPassthroughHandle is PRP 08's regression test.
// Before the revocable handle wrapper, an fd opened while its file was
// ALLOWED kept serving raw bytes through subsequent reads even after a
// policy reload tightened the file to HIDDEN — because a passthrough fd
// is LoopbackFile's raw fd, with no interception on Read. With the
// wrapper, a Read that fires after the reload re-consults the engine and
// fails closed.
func TestReloadRevokesOpenPassthroughHandle(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	target := filepath.Join(src, "become-hidden.txt")
	writeFixture(t, target, "secret plaintext that must not leak after reload")

	a, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// Open BEFORE any rule exists — the file is ALLOWED, the fd is a
	// passthrough handle.
	fd, err := os.Open(filepath.Join(mountpoint, "become-hidden.txt"))
	if err != nil {
		t.Fatalf("open (pre-reload): %v", err)
	}
	defer fd.Close()

	// First read succeeds — still ALLOWED.
	buf := make([]byte, 64)
	if n, err := fd.ReadAt(buf, 0); err != nil && !(err == io.EOF && n > 0) {
		t.Fatalf("first read (pre-reload): %v", err)
	}

	// Tighten policy on the fly: add the file to .janusfs.yml, reload.
	writeFixture(t, filepath.Join(src, ".janusignore"), "become-hidden.txt\n")
	if err := a.Engine.Reload(src); err != nil {
		t.Fatalf("Engine.Reload: %v", err)
	}

	// Second read on the SAME already-open fd must fail closed — the
	// revocable wrapper re-consults the engine per read and finds the
	// decision is no longer ALLOWED.
	buf2 := make([]byte, 64)
	_, err = fd.ReadAt(buf2, 0)
	if err == nil {
		t.Fatalf("second read after reload succeeded and returned %q — the open handle was NOT revoked", buf2)
	}
	if !os.IsPermission(err) {
		t.Errorf("expected EACCES after reload, got %v", err)
	}
}
