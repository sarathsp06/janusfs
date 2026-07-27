//go:build fuseintegration && (darwin || linux)

package mount

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestReadTOCTOUWindowClosed is PRP 05's regression test. Before descriptor-
// relative backing, a masked read decided against one resolution of a path
// and then re-opened the path string for its bytes; swapping the path to a
// symlink pointing at a hidden file between those two steps served the
// hidden file's plaintext under the earlier decision. With Backing.OpenAt
// + O_NOFOLLOW, the retained descriptor sees the file that was actually
// checked, and a swapped-in symlink at the final component fails the open
// rather than being silently followed.
//
// The race is made deterministic via the testRaceHook seam in
// maskedHandle.Read (nil in production), which fires exactly between the
// decision and the backing I/O.
func TestReadTOCTOUWindowClosed(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, ".janusignore"), "secret.env\n")
	writeFixture(t, filepath.Join(src, ".janusmask"), "data.txt : env-value\n")
	writeFixture(t, filepath.Join(src, "data.txt"), "public content\n")
	writeFixture(t, filepath.Join(src, "secret.env"), "API_KEY=must-not-leak\n")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// Between the decision (data.txt is Masked, but its content is boring) and
	// the actual read, swap data.txt on the real backing disk for a symlink
	// to secret.env. If backing access were path-based, the read would follow
	// the swapped symlink and leak secret.env's plaintext under data.txt's
	// (already-decided) rule set.
	var once sync.Once
	testRaceHook = func() {
		once.Do(func() {
			_ = os.Remove(filepath.Join(src, "data.txt"))
			_ = os.Symlink("secret.env", filepath.Join(src, "data.txt"))
		})
	}
	defer func() { testRaceHook = nil }()

	buf, err := os.ReadFile(filepath.Join(mountpoint, "data.txt"))
	// Either outcome closes the window: an error (O_NOFOLLOW rejects the
	// swapped-in symlink) or a successful read that does NOT contain the
	// secret. What must NEVER happen is a successful read whose bytes are
	// secret.env's plaintext.
	if err == nil && bytes.Contains(buf, []byte("must-not-leak")) {
		t.Fatalf("TOCTOU: read returned secret.env's plaintext through data.txt: %q", buf)
	}
}
