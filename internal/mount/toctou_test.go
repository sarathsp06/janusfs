//go:build fuseintegration && (darwin || linux)

package mount

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
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

// TestStalePolicyPickedUpOnOpen is the end-to-end regression for the
// stale-rules-fail-open fix: editing (or adding) a .janusfs.yml
// file on disk must be picked up the next time anything is opened near it —
// without an explicit `janusfs update` — because Open/OpendirHandle now
// probe for on-disk config staleness before resolving.
func TestStalePolicyPickedUpOnOpen(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()

	writeFixture(t, filepath.Join(src, "secret.env"), "API_KEY=must-not-leak\n")

	_, cleanup := mountForTest(t, src, mountpoint)
	defer cleanup()

	// Before any rule exists, the file reads through untouched.
	buf, err := os.ReadFile(filepath.Join(mountpoint, "secret.env"))
	if err != nil {
		t.Fatalf("expected secret.env to be readable before any rule exists: %v", err)
	}
	if !bytes.Contains(buf, []byte("must-not-leak")) {
		t.Fatalf("expected raw content before any rule exists, got %q", buf)
	}

	// Add a brand-new policy file directly on the real backing disk — no
	// `janusfs update`, no daemon restart. The next open must see it.
	writeFixture(t, filepath.Join(src, ".janusignore"), "secret.env\n")

	_, err = os.ReadFile(filepath.Join(mountpoint, "secret.env"))
	if err == nil {
		t.Fatal("expected the newly-added .janusfs.yml to take effect on the next open (EACCES), got a successful read")
	}
}

// TestStalePolicyAutoReloadUsesAdapterReload pins the drift the self-review
// found: the automatic on-open stale-policy reload must go through the same
// Adapter.Reload callback the daemon's manual `janusfs update` path uses,
// rather than calling Engine.Reload directly and silently skipping the shared
// side effects behind mountRuntime.reload.
func TestStalePolicyAutoReloadUsesAdapterReload(t *testing.T) {
	src := t.TempDir()
	mountpoint := t.TempDir()
	writeFixture(t, filepath.Join(src, "secret.env"), "API_KEY=must-not-leak\n")

	t.Setenv("HOME", t.TempDir())
	eng, err := engine.New(src)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	prov := provider.NewRamCache(64<<20, 32<<20, 64<<20)

	var reloads atomic.Int32
	a := &Adapter{
		Engine:   eng,
		Provider: prov,
		Reload: func() error {
			reloads.Add(1)
			return eng.Reload(src)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	mounted := make(chan struct{})
	a.OnMounted = func() { close(mounted) }
	done := make(chan error, 1)
	go func() { done <- a.Mount(ctx, src, mountpoint) }()

	select {
	case <-mounted:
	case err := <-done:
		t.Skipf("mount did not come up (macFUSE/FUSE not installed/approved?): %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Skip("mount did not come up within 5s (macFUSE/FUSE not installed/approved?)")
	}
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = a.Unmount(mountpoint)
			<-done
		}
	}()

	mountedPath := filepath.Join(mountpoint, "secret.env")
	if _, err := os.ReadFile(mountedPath); err != nil {
		t.Fatalf("expected secret.env to be readable before any rule exists: %v", err)
	}

	writeFixture(t, filepath.Join(src, ".janusignore"), "secret.env\n")

	if _, err := os.ReadFile(mountedPath); err == nil {
		t.Fatal("expected secret.env to be denied after auto reload, but the read succeeded")
	}
	if got := reloads.Load(); got == 0 {
		t.Fatal("expected automatic stale-policy reload to call Adapter.Reload, got 0 calls")
	}
}
