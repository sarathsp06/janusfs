package engine

import (
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
	// The atomic.Pointer swap is the concurrency contract (SPEC §7:
	// "readers never lock"); exercise it under -race.
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

func TestEngineSymlinkTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	// Create a secret file outside the project root
	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "id_rsa")
	if err := os.WriteFile(secretFile, []byte("private key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// In project root, define security rules:
	// We want to hide/mask id_rsa files
	writeFile(t, filepath.Join(root, ".janusignore"), "id_rsa\n")

	// Create a symlink in the project root pointing to the outside secret file
	symlinkPath := filepath.Join(root, "symkey")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Fatal(err)
	}

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// When we resolve "symkey" (which is a symlink to outside/id_rsa),
	// it should resolve to the canonical physical target, evaluate against the rules, and hide it!
	res := e.Resolve("symkey", false)
	if res.Decision != Hidden {
		t.Errorf("expected symlink target outside root to be Hidden, got %v", res.Decision)
	}
}
