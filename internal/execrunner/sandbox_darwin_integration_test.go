//go:build darwin && fuseintegration

// Reproduces the feasibility spike (docs/SEATBELT_SPIKE.md) against a real
// sandbox-exec, over two temp dirs standing in for the real source and the
// mountpoint. Run with: go test -tags fuseintegration ./internal/execrunner/...
package execrunner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxExecConfinesSourceTree(t *testing.T) {
	if err := sandboxAvailable(); err != nil {
		t.Skipf("sandbox-exec not available: %v", err)
	}

	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	mountDir := filepath.Join(dir, "mount")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountDir, 0o755); err != nil {
		t.Fatal(err)
	}

	secretPath := filepath.Join(realDir, ".env")
	if err := os.WriteFile(secretPath, []byte("API_KEY=supersecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	mountFile := filepath.Join(mountDir, "ok.txt")
	if err := os.WriteFile(mountFile, []byte("hello=world"), 0o644); err != nil {
		t.Fatal(err)
	}

	denyTargets, err := canonicalDenyTargets(realDir)
	if err != nil {
		t.Fatalf("canonicalDenyTargets: %v", err)
	}
	mustAllow, err := canonicalizeWithFirmlinkTwin(mountDir)
	if err != nil {
		t.Fatalf("canonicalizeWithFirmlinkTwin(mountDir): %v", err)
	}
	profile, err := sandboxProfile(denyTargets, nil, mustAllow)
	if err != nil {
		t.Fatalf("sandboxProfile: %v", err)
	}

	run := func(t *testing.T, shellCmd string) (string, error) {
		t.Helper()
		cmd := exec.Command(sandboxExecPath, "-p", profile, "--", "/bin/bash", "-c", shellCmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("direct read of real secret is denied", func(t *testing.T) {
		out, err := run(t, "cat "+secretPath)
		if err == nil {
			t.Fatalf("expected denial, command succeeded with output: %q", out)
		}
		if !strings.Contains(out, "Operation not permitted") {
			t.Errorf("expected 'Operation not permitted', got: %q", out)
		}
	})

	t.Run("read through the mount is allowed", func(t *testing.T) {
		out, err := run(t, "cat "+mountFile)
		if err != nil {
			t.Fatalf("expected mount read to succeed, got error: %v, output: %q", err, out)
		}
		if !strings.Contains(out, "hello=world") {
			t.Errorf("expected mount file contents, got: %q", out)
		}
	})

	t.Run("child process reading real secret is denied", func(t *testing.T) {
		out, err := run(t, "bash -c 'cat "+secretPath+"'")
		if err == nil {
			t.Fatalf("expected child denial, command succeeded with output: %q", out)
		}
		if !strings.Contains(out, "Operation not permitted") {
			t.Errorf("expected 'Operation not permitted' from child, got: %q", out)
		}
	})

	t.Run("grandchild process reading real secret is denied", func(t *testing.T) {
		out, err := run(t, "find "+realDir+" -type f -exec cat {} \\;")
		if err == nil {
			t.Fatalf("expected grandchild denial, command succeeded with output: %q", out)
		}
		if !strings.Contains(out, "Operation not permitted") {
			t.Errorf("expected 'Operation not permitted' from grandchild, got: %q", out)
		}
	})

	t.Run("write to real source path is denied", func(t *testing.T) {
		before, err := os.ReadFile(secretPath)
		if err != nil {
			t.Fatal(err)
		}
		_, err = run(t, "echo pwned >> "+secretPath)
		if err == nil {
			t.Fatal("expected write denial, command succeeded")
		}
		after, err := os.ReadFile(secretPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("real secret was modified: before=%q after=%q", before, after)
		}
	})

	t.Run("write into the mount is allowed", func(t *testing.T) {
		out, err := run(t, "echo more >> "+mountFile+" && echo WROTE-OK")
		if err != nil {
			t.Fatalf("expected mount write to succeed, got error: %v, output: %q", err, out)
		}
		if !strings.Contains(out, "WROTE-OK") {
			t.Errorf("expected WROTE-OK, got: %q", out)
		}
	})

	t.Run("nested attempt to loosen the sandbox is refused", func(t *testing.T) {
		out, err := run(t, sandboxExecPath+` -p '(version 1)(allow default)' cat `+secretPath)
		if err == nil {
			t.Fatalf("expected nested sandbox_apply to be refused, command succeeded with output: %q", out)
		}
		if !strings.Contains(out, "not permitted") && !strings.Contains(out, "sandbox_apply") {
			t.Errorf("expected a sandbox_apply / not-permitted failure, got: %q", out)
		}
	})

	t.Run("positive path: an ordinary command still succeeds under the profile", func(t *testing.T) {
		// Guards against a profile that denies the source so broadly it
		// breaks basic tool use inside the mount — "cat denied" and "usable
		// dev environment" are separate claims, and only this test checks
		// the second one.
		out, err := run(t, "cd "+mountDir+" && ls && echo LIST-OK")
		if err != nil {
			t.Fatalf("expected an ordinary command in the mount to succeed, got error: %v, output: %q", err, out)
		}
		if !strings.Contains(out, "LIST-OK") {
			t.Errorf("expected LIST-OK, got: %q", out)
		}
	})
}

// TestSandboxExecMountpointNestedUnderReadOnlyDenyStillWorks is a regression
// test for a real bug found by end-to-end testing of PRP 09: the default
// mount root is ~/.janusfs/mounts/..., so denying ~/.janusfs for
// defense-in-depth also denied the mountpoint itself whenever the user
// hasn't customized --root — breaking the one thing --sandbox promises to
// leave alone. sandboxProfile's fix is to re-allow the mountpoint last, so
// it wins over any earlier deny rule that happens to be an ancestor of it.
// This test reproduces the exact layout (mount nested under the read-only
// deny target) against real sandbox-exec.
func TestSandboxExecMountpointNestedUnderReadOnlyDenyStillWorks(t *testing.T) {
	if err := sandboxAvailable(); err != nil {
		t.Skipf("sandbox-exec not available: %v", err)
	}

	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	// janusHome/mounts/proj mirrors ~/.janusfs/mounts/<project> — the
	// mountpoint lives INSIDE the directory being read-only denied.
	janusHome := filepath.Join(dir, "janusHome")
	mountDir := filepath.Join(janusHome, "mounts", "proj")
	for _, d := range []string{realDir, mountDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	secretPath := filepath.Join(realDir, ".env")
	if err := os.WriteFile(secretPath, []byte("API_KEY=supersecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	mountFile := filepath.Join(mountDir, "ok.txt")
	if err := os.WriteFile(mountFile, []byte("hello=world"), 0o644); err != nil {
		t.Fatal(err)
	}

	denyRW, err := canonicalDenyTargets(realDir)
	if err != nil {
		t.Fatalf("canonicalDenyTargets: %v", err)
	}
	// canonicalReadOnlyDenyTargets looks for "<home>/.janusfs" specifically;
	// build the read-only deny set directly against janusHome here, since
	// janusHome itself is standing in for ~/.janusfs in this test.
	denyRO, err := canonicalizeWithFirmlinkTwin(janusHome)
	if err != nil {
		t.Fatalf("canonicalizeWithFirmlinkTwin(janusHome): %v", err)
	}
	mustAllow, err := canonicalizeWithFirmlinkTwin(mountDir)
	if err != nil {
		t.Fatalf("canonicalizeWithFirmlinkTwin(mountDir): %v", err)
	}
	profile, err := sandboxProfile(denyRW, denyRO, mustAllow)
	if err != nil {
		t.Fatalf("sandboxProfile: %v", err)
	}

	run := func(shellCmd string) (string, error) {
		cmd := exec.Command(sandboxExecPath, "-p", profile, "--", "/bin/bash", "-c", shellCmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("mountpoint under the read-only-denied home is still readable", func(t *testing.T) {
		out, err := run("cat " + mountFile)
		if err != nil {
			t.Fatalf("expected mount read to succeed despite nesting under a denied ancestor, got error: %v, output: %q", err, out)
		}
		if !strings.Contains(out, "hello=world") {
			t.Errorf("expected mount file contents, got: %q", out)
		}
	})

	t.Run("mountpoint under the read-only-denied home is still writable", func(t *testing.T) {
		out, err := run("echo more >> " + mountFile + " && echo WROTE-OK")
		if err != nil {
			t.Fatalf("expected mount write to succeed despite nesting under a denied ancestor, got error: %v, output: %q", err, out)
		}
		if !strings.Contains(out, "WROTE-OK") {
			t.Errorf("expected WROTE-OK, got: %q", out)
		}
	})

	t.Run("the real secret outside the mount is still denied", func(t *testing.T) {
		out, err := run("cat " + secretPath)
		if err == nil {
			t.Fatalf("expected denial, command succeeded with output: %q", out)
		}
		if !strings.Contains(out, "Operation not permitted") {
			t.Errorf("expected 'Operation not permitted', got: %q", out)
		}
	})
}
