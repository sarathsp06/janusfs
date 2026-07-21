package main

import (
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRuntime builds a mountRuntime with just the fields the daemon's
// status/index/list code reads — no FUSE, no goroutines.
func fakeRuntime(src, mp string, port int, token string) *mountRuntime {
	return &mountRuntime{Src: src, Mountpoint: mp, UIPort: port, Token: token}
}

func TestDaemonIndex_RendersAndEscapes(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{
		"/mnt/a": fakeRuntime("/src/<script>", "/mnt/a", 1234, "tok-abc"),
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
	if strings.Contains(body, "<script>") {
		t.Errorf("index did not escape source path: %q", body)
	}
	if !strings.Contains(body, "1 mount(s)") {
		t.Errorf("index missing mount count, got %q", body)
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

func TestDoMount_RequiresSrc(t *testing.T) {
	d := &daemon{mounts: map[string]*mountRuntime{}}
	resp := d.doMount(daemonRequest{})
	if resp.OK || !strings.Contains(resp.Error, "src is required") {
		t.Errorf("doMount with no src = %+v, want 'src is required' error", resp)
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
		"/mnt/a": fakeRuntime("/src/a", "/mnt/a", 7, "t"),
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
