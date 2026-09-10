//go:build linux && fuseintegration

package mount

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// TestDirectOvermount answers the question PRP 04 deferred: does the adapter
// deadlock when the FUSE mount is established directly OVER its own backing
// source (mountpoint == src), with no shadow bind mount in between?
//
// The current nsmount implementation avoids the question entirely by backing
// the adapter with a private bind mount of src (see
// docs/knowledge/platform-isolation.md, "As implemented"). If this test
// passes, that shadow mount is proven unnecessary and can be deleted; if it
// fails or times out, the shadow mount is proven load-bearing. Either result
// is valuable — which is why the test reports, rather than asserts, the
// safe-to-simplify outcome.
//
// The backing layer opens its dirfd BEFORE fs.Mount covers the path, so reads
// are expected to reach the real file even once the path itself resolves to
// the mount. A deadlock, if any, would come from a residual path-based access
// after mount time.
func TestDirectOvermount(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".janusfs.yml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(src)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	prov := provider.NewRamCache(64<<20, 32<<20, 64<<20)

	a := &Adapter{Engine: eng, Provider: prov}
	ctx, cancel := context.WithCancel(context.Background())
	mounted := make(chan struct{})
	a.OnMounted = func() { close(mounted) }

	done := make(chan error, 1)
	go func() { done <- a.Mount(ctx, src, src) }()

	// A deadlocked mount cannot be unmounted normally; -uz (lazy) detaches it
	// regardless, so the runner is never left with a hung mountpoint.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = exec.Command("fusermount3", "-uz", src).Run()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	})

	select {
	case <-mounted:
	case err := <-done:
		t.Skipf("mount did not come up (FUSE unavailable?): %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("direct overmount: mount did not come up within 5s — likely recursion at mount time")
	}

	type readResult struct {
		data []byte
		err  error
	}
	res := make(chan readResult, 1)
	go func() {
		data, err := os.ReadFile(filepath.Join(src, "hello.txt"))
		res <- readResult{data, err}
	}()

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("read through direct overmount failed: %v", r.err)
		}
		if string(r.data) != "hello" {
			t.Fatalf("read through direct overmount = %q, want %q", r.data, "hello")
		}
		t.Log("direct overmount is safe: adapter.Mount(ctx, src, src) serves reads without recursion — the nsmount shadow bind mount can be simplified away")
	case <-time.After(10 * time.Second):
		t.Fatal("read through direct overmount hung — backing access re-enters the mount; the nsmount shadow bind mount is load-bearing, keep it")
	}
}
