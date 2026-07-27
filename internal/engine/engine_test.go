package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEngineResolveBasic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, "README.md"), "x")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Resolve("server.pem", false).Decision; got != Hidden {
		t.Errorf("expected Hidden, got %v", got)
	}
	if got := e.Resolve("README.md", false).Decision; got != Allowed {
		t.Errorf("expected Allowed, got %v", got)
	}
}

func TestEngineNewOnMissingRootIsAllowedByDefault(t *testing.T) {
	// A nonexistent root isn't a fatal error at construction (WalkDir
	// records it as a non-fatal DiscoverErr instead) — the resulting
	// engine has no rules, so everything resolves to Allowed. Pin that
	// behavior rather than tacitly assuming it.
	t.Setenv("HOME", t.TempDir())
	e, err := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("New should not fail on missing root, got %v", err)
	}
	if got := e.Resolve("anything.txt", false).Decision; got != Allowed {
		t.Errorf("expected Allowed with no rules, got %v", got)
	}
}

func TestEngineRuleSetExposesCurrentSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rs := e.RuleSet()
	if len(rs.IgnoreLevels) == 0 {
		t.Fatal("expected at least one ignore level in the exposed snapshot")
	}
}

func TestEngineConcurrentResolveWithReload(t *testing.T) {
	// The atomic.Pointer swap is the concurrency contract — readers never lock —
	// so exercise it under -race.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				close(done)
				return
			default:
				_ = e.Resolve("a.txt", false)
			}
		}
	}()

	for i := 0; i < 10; i++ {
		if err := e.Reload(root); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	if e.Generation() != 11 {
		t.Errorf("Generation after 10 reloads = %d, want 11", e.Generation())
	}
}

func TestEngineGenerationBumpsOnReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if e.Generation() != 1 {
		t.Fatalf("expected initial generation 1, got %d", e.Generation())
	}

	if got := e.Resolve("a.txt", false).Decision; got != Allowed {
		t.Fatalf("expected Allowed before reload, got %v", got)
	}

	writeFile(t, filepath.Join(root, ".janusignore"), "a.txt\n")
	if err := e.Reload(root); err != nil {
		t.Fatal(err)
	}
	if e.Generation() != 2 {
		t.Fatalf("expected generation 2 after reload, got %d", e.Generation())
	}
	if got := e.Resolve("a.txt", false).Decision; got != Hidden {
		t.Fatalf("expected Hidden after reload picked up new rule, got %v", got)
	}
}

func TestEngineResolveCacheHitMatchesMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".janusignore"), "*.pem\n")
	writeFile(t, filepath.Join(root, ".janusmask"), "*.env : env-value\n")
	writeFile(t, filepath.Join(root, "server.pem"), "x")
	writeFile(t, filepath.Join(root, "secret.env"), "API_KEY=x")
	writeFile(t, filepath.Join(root, "README.md"), "x")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want Decision
	}{
		{"README.md", Allowed},
		{"secret.env", Masked},
		{"server.pem", Hidden},
	}
	for _, c := range cases {
		miss := e.Resolve(c.path, false) // first call: cache miss
		hit := e.Resolve(c.path, false)  // second call: cache hit
		if miss.Decision != c.want {
			t.Errorf("%s: miss decision = %v, want %v", c.path, miss.Decision, c.want)
		}
		if hit.Decision != miss.Decision {
			t.Errorf("%s: hit decision %v != miss decision %v", c.path, hit.Decision, miss.Decision)
		}
		if hit.Generation != miss.Generation {
			t.Errorf("%s: hit generation %d != miss generation %d", c.path, hit.Generation, miss.Generation)
		}
	}
}

func TestEngineResolveCacheDoesNotGrowUnbounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Resolve far more distinct (nonexistent) paths than decisionCacheMax, the
	// way an untrusted caller stat-ing unlimited distinct paths would. This
	// must not panic, hang, or (once the cache resets past the bound) return a
	// wrong answer for the one real path that matters.
	for i := 0; i < decisionCacheMax*2+100; i++ {
		_ = e.Resolve(fmt.Sprintf("nonexistent-%d.txt", i), false)
	}

	if got := e.Resolve("a.txt", false).Decision; got != Allowed {
		t.Errorf("expected Allowed for a.txt after cache overflow, got %v", got)
	}
	if e.cacheEntries.Load() > decisionCacheMax {
		t.Errorf("cacheEntries = %d, want <= %d after overflow reset", e.cacheEntries.Load(), decisionCacheMax)
	}
}

func BenchmarkResolveCacheHit(b *testing.B) {
	home, err := os.MkdirTemp("", "janusfs-bench-home")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(home)
	b.Setenv("HOME", home)

	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		b.Fatal(err)
	}
	e, err := New(root)
	if err != nil {
		b.Fatal(err)
	}
	e.Resolve("a.txt", false) // warm the cache

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.Resolve("a.txt", false)
	}
}

func BenchmarkResolveCacheMiss(b *testing.B) {
	home, err := os.MkdirTemp("", "janusfs-bench-home")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(home)
	b.Setenv("HOME", home)

	root := b.TempDir()
	// A ten-level directory hierarchy, each with its own .janusignore, so
	// resolving the deepest path exercises a ten-level miss.
	dir := root
	for i := 0; i < 10; i++ {
		dir = filepath.Join(dir, fmt.Sprintf("level%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".janusignore"), []byte("*.tmp\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		b.Fatal(err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		b.Fatal(err)
	}

	e, err := New(root)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Force a fresh generation (and therefore a fresh cache key) every
		// iteration, so each call is a genuine miss rather than hitting the
		// entry from the previous iteration.
		e.gen.Add(1)
		_ = e.Resolve(rel, false)
	}
}
