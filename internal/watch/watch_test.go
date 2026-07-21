package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForEvent reads from ch within a deadline and returns the value, or
// fails the test if the deadline expires. Tests that depend on fsnotify
// event delivery timing use this rather than hard-coded sleeps.
func waitForEvent(t *testing.T, ch <-chan string, expected string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-ch:
			if got == expected {
				return
			}
			// Got a different event; keep waiting for the expected one.
			continue
		case <-deadline:
			t.Fatalf("timed out waiting for %q", expected)
		}
	}
}

func TestNewAndStop(t *testing.T) {
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// Stop on a fresh watcher (never Started) must not panic.
	w.Stop()
}

func TestStartStopCleanly(t *testing.T) {
	root := t.TempDir()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configCh := make(chan string, 16)
	dataCh := make(chan string, 16)

	w.Start(root,
		func(path string) { configCh <- path },
		func(path string, op Op) { dataCh <- path },
	)

	// Write a file to prove the watch loop is running.
	writeFile(t, filepath.Join(root, "test.txt"), "x")
	waitForEvent(t, dataCh, filepath.Join(root, "test.txt"))

	w.Stop()

	// After Stop, no more events arrive.
	prev := len(configCh) + len(dataCh)
	time.Sleep(30 * time.Millisecond)
	if len(configCh)+len(dataCh) != prev {
		t.Error("events still arriving after Stop")
	}
}

func TestConfigEvent(t *testing.T) {
	root := t.TempDir()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configCh := make(chan string, 16)
	dataCh := make(chan string, 16)

	w.Start(root,
		func(path string) { configCh <- path },
		func(path string, op Op) { dataCh <- path },
	)
	defer w.Stop()

	// Verify the watcher is ready by writing a known file and waiting.
	probe := filepath.Join(root, ".probe")
	writeFile(t, probe, "")
	waitForEvent(t, dataCh, probe)

	// Create a .janusignore file — must trigger onConfig.
	ignorePath := filepath.Join(root, ".janusignore")
	writeFile(t, ignorePath, "*.pem\n")
	waitForEvent(t, configCh, ignorePath)

	// Create a .janusmask file — must also trigger onConfig.
	maskPath := filepath.Join(root, ".janusmask")
	writeFile(t, maskPath, "*.env: env-value\n")
	waitForEvent(t, configCh, maskPath)
}

func TestDataEvent(t *testing.T) {
	root := t.TempDir()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configCh := make(chan string, 16)
	dataCh := make(chan string, 16)

	w.Start(root,
		func(path string) { configCh <- path },
		func(path string, op Op) { dataCh <- path },
	)
	defer w.Stop()

	// Verify watcher is ready.
	probe := filepath.Join(root, ".probe")
	writeFile(t, probe, "")
	waitForEvent(t, dataCh, probe)

	// Create a regular file — must trigger onData.
	dataPath := filepath.Join(root, "README.md")
	writeFile(t, dataPath, "hello")
	waitForEvent(t, dataCh, dataPath)
}

// TestConfigEditInSubdirFires proves the config-focused contract: a directory
// that already holds a config file is watched, so edits to that config file
// are detected (the recursive auto-watch of arbitrary new subdirs was dropped
// to keep descriptor use bounded — see Start).
func TestConfigEditInSubdirFires(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(subdir, ".janusmask")
	writeFile(t, cfg, "*\n") // present before Start → sub/ is watched

	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configCh := make(chan string, 16)
	w.Start(root,
		func(path string) { configCh <- path },
		func(path string, op Op) {},
	)
	defer w.Stop()

	// Editing the existing config file in the watched subdir fires onConfig.
	writeFile(t, cfg, "*.env\n")
	waitForEvent(t, configCh, cfg)
}

func TestStats(t *testing.T) {
	root := t.TempDir()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	configCh := make(chan string, 16)
	dataCh := make(chan string, 16)

	w.Start(root,
		func(path string) { configCh <- path },
		func(path string, op Op) { dataCh <- path },
	)
	defer w.Stop()

	// Verify watcher is ready.
	probe := filepath.Join(root, ".probe")
	writeFile(t, probe, "")
	waitForEvent(t, dataCh, probe)

	// Write a config file and a data file, then check stats.
	writeFile(t, filepath.Join(root, ".janusignore"), "*\n")
	writeFile(t, filepath.Join(root, "data.txt"), "x")
	waitForEvent(t, configCh, filepath.Join(root, ".janusignore"))
	waitForEvent(t, dataCh, filepath.Join(root, "data.txt"))

	// Allow stats to settle.
	time.Sleep(20 * time.Millisecond)
	s := w.Stats()
	if s.ConfigEvents == 0 {
		t.Error("expected ConfigEvents > 0")
	}
	if s.DataEvents == 0 {
		t.Error("expected DataEvents > 0")
	}
	// EventsTotal should reflect both events (config + data), but there may
	// also be probe events. Just check it's >= 2.
	if s.EventsTotal < 2 {
		t.Errorf("expected EventsTotal >= 2, got %d", s.EventsTotal)
	}
	if s.WatchedDirs == 0 {
		t.Error("expected WatchedDirs > 0")
	}
}

// contains reports whether the watch list includes an exact path.
func watchListHas(w *Watcher, path string) bool {
	for _, p := range w.w.WatchList() {
		if p == path {
			return true
		}
	}
	return false
}

func TestConfigDirs_WatchesOnlyConfigBearingDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"src", "sub", "node_modules/pkg", ".git/objects"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Config files: at root, in sub/ (watched), and inside node_modules (skipped).
	writeFile(t, filepath.Join(root, ".janusmask"), "*\n")
	writeFile(t, filepath.Join(root, "sub", ".janusignore"), "*\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", ".janusmask"), "*\n")

	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	w.Start(root, func(string) {}, func(string, Op) {})
	defer w.Stop()

	if !watchListHas(w, root) {
		t.Error("root should always be watched")
	}
	if !watchListHas(w, filepath.Join(root, "sub")) {
		t.Error("config-bearing dir sub/ should be watched")
	}
	if watchListHas(w, filepath.Join(root, "src")) {
		t.Error("src/ has no config file and should NOT be watched (config-focused)")
	}
	if watchListHas(w, filepath.Join(root, "node_modules", "pkg")) {
		t.Error("config file inside a skipped dir must not cause it to be watched")
	}
	if got := len(w.w.WatchList()); got != 2 {
		t.Errorf("watched %d dirs, want 2 (root + sub); list=%v", got, w.w.WatchList())
	}
	if w.Stats().Limited {
		t.Error("small tree should not report Limited")
	}
}

func TestConfigDirs_CustomSkipDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"boring", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "boring", ".janusmask"), "*\n")
	writeFile(t, filepath.Join(root, "node_modules", ".janusmask"), "*\n")

	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	w.SkipDirs = []string{"boring"} // replaces defaults: node_modules NOT skipped now
	w.Start(root, func(string) {}, func(string, Op) {})
	defer w.Stop()

	if !watchListHas(w, filepath.Join(root, "node_modules")) {
		t.Error("with a custom skip set omitting node_modules, its config dir should be watched")
	}
	if watchListHas(w, filepath.Join(root, "boring")) {
		t.Error("custom skip set should have skipped 'boring'")
	}
}

func TestConfigDirs_CapsAndReportsLimited(t *testing.T) {
	root := t.TempDir()
	// More config-bearing dirs than the cap so expansion must stop early.
	for i := 0; i < maxWatchedDirs+50; i++ {
		d := filepath.Join(root, "d"+itoa(i))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, ".janusmask"), "*\n")
	}
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	w.Start(root, func(string) {}, func(string, Op) {})
	defer w.Stop()

	if n := len(w.w.WatchList()); n > maxWatchedDirs {
		t.Errorf("watched %d dirs, exceeds cap %d", n, maxWatchedDirs)
	}
	if !w.Stats().Limited {
		t.Error("expected Limited=true once the cap is hit")
	}
}

// itoa avoids importing strconv just for the cap test's directory names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func TestIsConfigFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"/some/dir/.janusignore", true},
		{"/some/dir/.janusmask", true},
		{".janusignore", true},
		{".janusmask", true},
		{"/some/dir/file.txt", false},
		{"/some/dir/JANUSIGNORE", false},
		{"/some/dir/.janusignore.backup", false},
		{"/some/dir/.janusmask.v1", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isConfigFile(tt.name); got != tt.want {
			t.Errorf("isConfigFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestOpString(t *testing.T) {
	tests := []struct {
		op   Op
		want string
	}{
		{Create, "CREATE"},
		{Write, "WRITE"},
		{Remove, "REMOVE"},
		{Rename, "RENAME"},
		{Chmod, "CHMOD"},
		{Op(0), "UNKNOWN"},
		{Create | Write, "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("Op(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
