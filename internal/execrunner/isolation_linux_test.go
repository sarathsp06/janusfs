//go:build linux && fuseintegration

// This file exercises the actual `janusfs` binary as a subprocess (`go build`
// then invoke), never `execrunner.Run` in-process, because Run's namespace
// path re-execs `os.Executable()` as `janusfs __nsmount` — under `go test`,
// `os.Executable()` resolves to the test binary itself, which has no
// __nsmount subcommand. Building and exec-ing the real binary is the only way
// to exercise the real re-exec path end to end.
//
// NOTE: written and reviewed against documented Linux namespace semantics,
// but not executed on a real Linux machine as part of this change — the
// development environment this was authored in is darwin-only, where
// CLONE_NEWNS/CLONE_NEWUSER don't exist even for testing. Run this suite on
// an actual Linux box (`make integration` under a Linux CI runner, or
// `go test -tags fuseintegration ./internal/execrunner/...` on a Linux
// machine with /dev/fuse available) before relying on it as a pass/fail gate.
package execrunner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const unsupportedPrivateMountMsg = "nsmount: making the mount tree recursively private: permission denied"

func skipIfUnsupportedPrivateMount(t *testing.T, err error, output string) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(output, unsupportedPrivateMountMsg) {
		t.Skip("runner kernel does not allow recursively-private remount inside this unprivileged namespace")
	}
}

// buildJanusfsBinary compiles cmd/janusfs into a temp directory and returns
// its path. Cached per test run via t.TempDir() (not across tests) since the
// build is what's under test indirectly — a stale binary would be the wrong
// thing to test against.
func buildJanusfsBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "janusfs")

	// Locate the module root (this file lives at internal/execrunner/), so
	// `go build` runs against the real module regardless of the test's cwd.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	moduleRoot := filepath.Join(wd, "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/janusfs")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building janusfs binary: %v\n%s", err, out)
	}
	return bin
}

// readMountinfo returns this (host) process's own mount table, byte for
// byte, as reported by the kernel. Used as a before/after snapshot: the
// entire point of the private-namespace model is that mounting inside the
// namespace of a CHILD process must be invisible here.
func readMountinfo(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatalf("reading /proc/self/mountinfo: %v", err)
	}
	return data
}

// TestNamespaceIsolation_HostMountTableUnaffected is the NFR-9 test: it must
// assert the NEGATIVE (the host cannot see the namespace's mount), not just
// that the child sees redacted content — a test that only checked the latter
// would pass even if the mount had propagated to the host, which is the exact
// bug MS_REC|MS_PRIVATE exists to prevent.
func TestNamespaceIsolation_HostMountTableUnaffected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only: private mount namespaces are a Linux kernel feature")
	}

	bin := buildJanusfsBinary(t)

	src := t.TempDir()
	secretPath := filepath.Join(src, "secret.env")
	const secretValue = "API_KEY=isolation-test-sentinel-9f8e7d"
	if err := os.WriteFile(secretPath, []byte(secretValue+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".janusfs.yml"), []byte("version: 1\nmask:\n  - paths:\n      - \"secret.env\"\n    patterns:\n      - env-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := readMountinfo(t)

	cmd := exec.Command(bin, "exec", "--", "cat", secretPath)
	cmd.Dir = src
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		skipIfUnsupportedPrivateMount(t, err, stderr.String())
		if err != nil {
			t.Fatalf("janusfs exec failed: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("janusfs exec did not complete within 15s")
	}

	// 1. The child, running inside the namespace, must see the MASKED
	// (redacted) content — not the real secret value.
	childOutput := stdout.String()
	if strings.Contains(childOutput, secretValue) {
		t.Fatalf("child saw the real secret value through the filtered view: %q", childOutput)
	}
	if !strings.Contains(childOutput, "API_KEY=") {
		t.Fatalf("expected the key name to survive redaction (env-value masks only the value), got: %q", childOutput)
	}

	// 2. The host (this test process), reading the same path directly, must
	// see the REAL content — proving the two views coexist, one is not
	// silently replacing the other.
	hostBytes, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("reading secret directly: %v", err)
	}
	if !strings.Contains(string(hostBytes), secretValue) {
		t.Fatalf("host-side read did not see the real secret value; got: %q", hostBytes)
	}

	// 3. THE ASSERTION THAT MATTERS: the host's own mount table is untouched.
	// If MS_REC|MS_PRIVATE were missing (or in the wrong place), the FUSE
	// mount established inside the child's namespace would propagate back
	// out and appear here too.
	after := readMountinfo(t)
	if !bytes.Equal(before, after) {
		t.Fatalf("host /proc/self/mountinfo changed during janusfs exec — the namespace mount propagated to the host, defeating isolation.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestNamespaceIsolation_NoDaemonRequired asserts `janusfs exec` succeeds with
// no daemon running at all — a hard requirement of the namespace model, since
// the exec process is its own FUSE server.
func TestNamespaceIsolation_NoDaemonRequired(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only")
	}
	bin := buildJanusfsBinary(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".janusfs.yml"), []byte("version: 1\nhide:\n  - \"*.secret\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deliberately isolate from any real daemon: an isolated HOME with no
	// daemon.sock at all.
	home := t.TempDir()

	cmd := exec.Command(bin, "exec", "--", "cat", filepath.Join(src, "hello.txt"))
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	skipIfUnsupportedPrivateMount(t, err, string(out))
	if err != nil {
		t.Fatalf("janusfs exec failed with no daemon running: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("expected to read hello.txt's content, got: %q", out)
	}
}

// TestNamespaceIsolation_ExitCodeAndSignals asserts the child's exit code
// propagates through both re-exec stages unchanged.
func TestNamespaceIsolation_ExitCodeAndSignals(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only")
	}
	bin := buildJanusfsBinary(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".janusfs.yml"), []byte("version: 1\nhide:\n  - \"*.secret\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "exec", "--", "sh", "-c", "exit 42")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	skipIfUnsupportedPrivateMount(t, err, string(out))
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected an *exec.ExitError, got %v (%T)", err, err)
	}
	if exitErr.ExitCode() != 42 {
		t.Fatalf("expected exit code 42, got %d", exitErr.ExitCode())
	}
}

// TestNamespaceIsolation_TeardownRestoresNormalAccess asserts that after
// janusfs exec exits, the source directory is readable normally and the host
// mount table is unaffected — the namespace and everything mounted within it
// having been reclaimed by the kernel.
func TestNamespaceIsolation_TeardownRestoresNormalAccess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only")
	}
	bin := buildJanusfsBinary(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".janusfs.yml"), []byte("version: 1\nhide:\n  - \"*.secret\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "after.txt"), []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := readMountinfo(t)

	cmd := exec.Command(bin, "exec", "--", "true")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		skipIfUnsupportedPrivateMount(t, err, string(out))
		t.Fatalf("janusfs exec failed: %v\n%s", err, out)
	}

	after := readMountinfo(t)
	if !bytes.Equal(before, after) {
		t.Fatalf("host mount table changed across an exec session's full lifecycle")
	}

	data, err := os.ReadFile(filepath.Join(src, "after.txt"))
	if err != nil {
		t.Fatalf("reading source directory after exec exited: %v", err)
	}
	if string(data) != "still here" {
		t.Fatalf("unexpected content after teardown: %q", data)
	}
}
