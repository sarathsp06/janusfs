package main

import (
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarathsp06/janusfs/internal/config"
)

// fakeRuntime builds a mountRuntime with just the fields the daemon's
// status/index/list code reads — no FUSE, no goroutines.
func fakeRuntime(src, mp, label string, port int, token string) *mountRuntime {
	return &mountRuntime{Src: src, Mountpoint: mp, Label: label, UIPort: port, Token: token}
}

func TestDaemonIndex_RendersLabelAndEscapes(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{
		"/mnt/a": fakeRuntime("/src/<script>", "/mnt/a", "My <b>Project</b>", 1234, "tok-abc"),
	}}
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	d.handleIndex(w, req)

	if w.Code != 200 {
		t.Fatalf("index status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "localhost:1234/?token=tok-abc") {
		t.Errorf("index missing per-mount dashboard link, got %q", body)
	}
	if !strings.Contains(body, "My &lt;b&gt;Project&lt;/b&gt;") {
		t.Errorf("index did not show the escaped label, got %q", body)
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "<b>Project") {
		t.Errorf("index did not escape user-supplied text: %q", body)
	}
	if !strings.Contains(body, "1 mount(s)") {
		t.Errorf("index missing mount count, got %q", body)
	}
}

func TestDaemonIndex_FallsBackToSrcWithoutLabel(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{
		"/mnt/a": fakeRuntime("/src/proj", "/mnt/a", "", 1, "t"),
	}}
	w := httptest.NewRecorder()
	d.handleIndex(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "/src/proj") {
		t.Errorf("index should fall back to src when no label; got %q", w.Body.String())
	}
}

func TestDaemonIndex_NotFoundForOtherPaths(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{}}
	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	d.handleIndex(w, req)
	if w.Code != 404 {
		t.Errorf("status for non-root path = %d, want 404", w.Code)
	}
}

func TestDoUnmount_NotMounted(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{}}
	resp := d.doUnmount(daemonRequest{Mountpoint: "/not/mounted"})
	if resp.OK {
		t.Fatal("doUnmount of an unknown path returned OK, want error")
	}
	if !strings.Contains(resp.Error, "not mounted") {
		t.Errorf("error = %q, want it to mention 'not mounted'", resp.Error)
	}
}

func TestDoUnmount_PrunesStaleRegistryEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A registry entry the daemon is NOT tracking live (e.g. failed to resume).
	if err := config.RecordMount("/some/src", "/some/mnt/point", "lbl"); err != nil {
		t.Fatal(err)
	}
	d := &daemon{mounts: map[string]*mountRuntime{}}

	resp := d.doUnmount(daemonRequest{Cmd: "unmount", Mountpoint: "/some/mnt/point"})
	if !resp.OK {
		t.Fatalf("expected OK pruning a stale entry, got %+v", resp)
	}
	if recs, _ := config.LoadMounts(); len(recs) != 0 {
		t.Errorf("stale entry not pruned from registry: %+v", recs)
	}
}

func TestDoUnmount_UnknownAndNotInRegistry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := &daemon{mounts: map[string]*mountRuntime{}}

	resp := d.doUnmount(daemonRequest{Cmd: "unmount", Mountpoint: "/nope"})
	if resp.OK {
		t.Fatal("expected an error for an unknown, unregistered path")
	}
	if !strings.Contains(resp.Error, "not in the registry") {
		t.Errorf("error = %q, want it to mention the registry", resp.Error)
	}
}

func TestDoMount_RequiresSrc(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{}}
	resp := d.doMount(daemonRequest{})
	if resp.OK || !strings.Contains(resp.Error, "src is required") {
		t.Errorf("doMount with no src = %+v, want 'src is required' error", resp)
	}
}

func TestDoMount_MissingSrc_CleanMessage(t *testing.T) {
	root := t.TempDir()
	d := &daemon{mounts: map[string]*mountRuntime{}, base: config.Config{MountRoot: root}}

	resp := d.doMount(daemonRequest{Cmd: "mount", Src: filepath.Join(t.TempDir(), "does-not-exist")})
	if resp.OK {
		t.Fatal("doMount of a missing source returned OK")
	}
	if !strings.Contains(resp.Error, "does not exist") {
		t.Errorf("error = %q, want a clean 'does not exist' message", resp.Error)
	}
	// The internal package prefix and raw syscall noise must not leak.
	if strings.Contains(resp.Error, "config:") || strings.Contains(resp.Error, "stat ") {
		t.Errorf("error leaks internal detail to the user: %q", resp.Error)
	}
}

func TestChildMountsUnder(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{
		"/root/a":       fakeRuntime("/src/a", "/root/a", "", 1, "t"),
		"/root/a/child": fakeRuntime("/src/child", "/root/a/child", "", 2, "t"),
		"/root/b":       fakeRuntime("/src/b", "/root/b", "", 3, "t"),
	}}
	got := d.childMountsUnder("/root/a")
	if len(got) != 1 || got[0] != "/src/child" {
		t.Errorf("childMountsUnder(/root/a) = %v, want [/src/child] (sibling /root/b and self excluded)", got)
	}
	if under := d.childMountsUnder("/root/b"); len(under) != 0 {
		t.Errorf("childMountsUnder(/root/b) = %v, want none", under)
	}
}

func TestDoMount_NestedChildRejected(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	d := &daemon{mounts: map[string]*mountRuntime{}, base: config.Config{MountRoot: root}}

	// Reproduce the daemon's derivation (mirror the symlink-resolved src under
	// the root) and plant a live child mount inside it, making the parent's
	// mountpoint non-empty exactly as a real child-first mount would.
	abs, _ := filepath.Abs(src)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	derived := filepath.Join(root, resolved)
	childMp := filepath.Join(derived, "child")
	if err := os.MkdirAll(childMp, 0o700); err != nil {
		t.Fatal(err)
	}
	d.mounts[childMp] = fakeRuntime(filepath.Join(src, "child"), childMp, "", 1, "t")

	resp := d.doMount(daemonRequest{Cmd: "mount", Src: src})
	if resp.OK {
		t.Fatal("doMount over a parent of a live mount returned OK, want a nested-mount error")
	}
	if !strings.Contains(resp.Error, "nested under it") || !strings.Contains(resp.Error, "umount") {
		t.Errorf("error = %q, want it to explain the nesting and suggest umount", resp.Error)
	}
}

func TestDaemonSocket_ListRoundTrip(t *testing.T) {
	// macOS caps unix-socket paths at ~104 bytes, and t.TempDir() is far too
	// long once ".janusfs/daemon.sock" is appended; use a short /tmp HOME.
	home, err := os.MkdirTemp("/tmp", "janus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)

	d := &daemon{mounts: map[string]*mountRuntime{
		"/mnt/a": fakeRuntime("/src/a", "/mnt/a", "", 7, "t"),
	}}

	sock, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go d.acceptLoop(ln)

	resp, err := daemonCall(daemonRequest{Cmd: "list"})
	if err != nil {
		t.Fatalf("daemonCall(list) error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("list resp not OK: %+v", resp)
	}
	if len(resp.Mounts) != 1 || resp.Mounts[0].Src != "/src/a" {
		t.Fatalf("list mounts = %+v, want one mount with src /src/a", resp.Mounts)
	}
}

func TestDaemonCall_NoDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no socket here
	_, err := daemonCall(daemonRequest{Cmd: "list"})
	if !errors.Is(err, errDaemonNotRunning) {
		t.Errorf("daemonCall with no daemon = %v, want errDaemonNotRunning", err)
	}
}

func TestRunMount_NoDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runMount(daemonRequest{Cmd: "mount", Src: "/some/src"})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Errorf("runMount with no daemon = %v, want an error mentioning the daemon", err)
	}
}

func TestHistoryDBPath_UniquePerSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := historyDBPath("/one/app")
	b := historyDBPath("/two/app") // same basename, different source
	if a == b {
		t.Fatalf("two sources with the same basename collided on %q", a)
	}
	for _, p := range []string{a, b} {
		if !strings.HasSuffix(p, ".db") || !strings.Contains(p, "app-") {
			t.Errorf("history path %q not of form <basename>-<hash>.db", p)
		}
	}
}

func TestRunPaths_ListsKnownLocations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := captureStdout(t, func() {
		if err := runPaths(); err != nil {
			t.Fatalf("runPaths() error = %v", err)
		}
	})
	for _, want := range []string{"settings", "mounts registry", "global rules", "mount root"} {
		if !strings.Contains(out, want) {
			t.Errorf("paths output missing %q; got:\n%s", want, out)
		}
	}
}
