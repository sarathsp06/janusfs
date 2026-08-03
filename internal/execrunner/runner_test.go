//go:build darwin

package execrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMockE2E(t *testing.T) {
	// Create temp home directory for the test.
	tmpHome, err := os.MkdirTemp("", "janusfs-test-home")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpHome) }()

	// Save original home and restore later.
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Define our source and mount directories. Resolve symlinks immediately:
	// on macOS, os.MkdirTemp returns a path under /var/folders/..., which is
	// itself a symlink to /private/var/folders/...; a child process's real
	// getcwd(2) reports the resolved form, so comparing against the
	// unresolved form would spuriously fail the CWD/rewrite assertions below.
	srcDirRaw, err := os.MkdirTemp("", "janusfs-src")
	if err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(srcDirRaw) }()
	srcDir, err := filepath.EvalSymlinks(srcDirRaw)
	if err != nil {
		t.Fatalf("failed to resolve src dir: %v", err)
	}

	mountDirRaw, err := os.MkdirTemp("", "janusfs-mount")
	if err != nil {
		t.Fatalf("failed to create mount dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(mountDirRaw) }()
	mountDir, err := filepath.EvalSymlinks(mountDirRaw)
	if err != nil {
		t.Fatalf("failed to resolve mount dir: %v", err)
	}

	// Create a dummy file in source.
	dummyFile := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(dummyFile, []byte("hello from src"), 0o644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	// A policy marker: findSourceAndMount refuses to provision a mount over a
	// directory with no .janusfs.yml ancestor, so this test's implicit "cwd has
	// policy" assumption must be made explicit.
	if err := os.WriteFile(filepath.Join(srcDir, ".janusfs.yml"), []byte("version: 1\nhide:\n  - \"*.secret\"\n"), 0o644); err != nil {
		t.Fatalf("failed to write .janusfs.yml: %v", err)
	}

	// Create dummy file in mount to simulate the FUSE view.
	mountDummyFile := filepath.Join(mountDir, "hello.txt")
	if err := os.WriteFile(mountDummyFile, []byte("hello from mount"), 0o644); err != nil {
		t.Fatalf("failed to write mount dummy file: %v", err)
	}

	// Synthesize .janusfs directory in mountDir so readiness polling passes immediately.
	vfsDir := filepath.Join(mountDir, ".janusfs")
	if err := os.MkdirAll(vfsDir, 0o755); err != nil {
		t.Fatalf("failed to create .janusfs dir: %v", err)
	}

	// Start a mock Unix socket daemon.
	sockPath := filepath.Join(tmpHome, ".janusfs", "daemon.sock")
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		t.Fatalf("failed to create socket dir: %v", err)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Handle mock daemon requests in a goroutine.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				var req daemonRequest
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}

				var resp daemonResponse
				switch req.Cmd {
				case "list":
					resp.OK = true
					resp.Mounts = []mountStatus{}
				case "mount":
					resp.OK = true
					resp.Mounts = []mountStatus{
						{
							Src:        srcDir,
							Mountpoint: mountDir,
						},
					}
				}
				_ = json.NewEncoder(c).Encode(resp)
			}(conn)
		}
	}()

	// Change working directory to srcDir to simulate running inside the source tree.
	origCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}
	if err := os.Chdir(srcDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCWD) }()

	// Set some environment variables.
	_ = os.Setenv("JANUSFS_MOCK_SECRET", "supersecret")
	_ = os.Setenv("MY_VAR", "myvalue")
	defer func() { _ = os.Unsetenv("JANUSFS_MOCK_SECRET") }()
	defer func() { _ = os.Unsetenv("MY_VAR") }()

	// Target command: compile a small Go program and execute it.
	const helperGo = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	cwd, _ := os.Getwd()
	fmt.Printf("CWD:%s\n", cwd)

	for _, arg := range os.Args[1:] {
		fmt.Printf("ARG:%s\n", arg)
	}

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "JANUSFS_") {
			fmt.Println("LEAK:JANUSFS_MOCK_SECRET is not scrubbed")
			os.Exit(1)
		}
	}
	fmt.Println("ENV_SCRUBBED:OK")
}
`
	testBinDir, err := os.MkdirTemp("", "janusfs-test-bin")
	if err != nil {
		t.Fatalf("failed to create test bin dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(testBinDir) }()

	goFile := filepath.Join(testBinDir, "helper.go")
	if err := os.WriteFile(goFile, []byte(helperGo), 0o644); err != nil {
		t.Fatalf("failed to write helper.go: %v", err)
	}

	binFile := filepath.Join(testBinDir, "helper")
	compileCmd := exec.Command("go", "build", "-o", binFile, goFile)
	if err := compileCmd.Run(); err != nil {
		t.Fatalf("failed to compile test helper: %v", err)
	}

	// Capture stdout and stderr of Run. We can swap os.Stdout and os.Stderr temporarily.
	origStdout := os.Stdout
	origStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	// Run our helper through execrunner.Run!
	// We pass an argument containing the absolute src path to verify translation.
	argWithSrc := filepath.Join(srcDir, "somefile.txt")
	exitCode, runErr := Run(context.Background(), []string{binFile, argWithSrc}, false)

	// Restore stdout/stderr
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr

	if runErr != nil {
		t.Fatalf("unexpected error running: %v", runErr)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Read captured outputs
	var stdoutBuf bytes.Buffer
	_, _ = stdoutBuf.ReadFrom(rOut)
	var stderrBuf bytes.Buffer
	_, _ = stderrBuf.ReadFrom(rErr)

	stdoutStr := stdoutBuf.String()
	t.Logf("Captured stdout:\n%s", stdoutStr)

	// Verify CWD hijacking: our compiled helper ran inside hijackedCWD, which is mountDir.
	// stdout is byte-faithful, so the child-visible mount path is not rewritten on output.
	if !strings.Contains(stdoutStr, "CWD:"+mountDir) {
		t.Errorf("expected CWD output to reference mountDir %q, stdout: %q", mountDir, stdoutStr)
	}
	if strings.Contains(stdoutStr, "CWD:"+srcDir) {
		t.Errorf("expected CWD output not to be rewritten to srcDir %q, stdout: %q", srcDir, stdoutStr)
	}

	// Verify argument path translation: the child receives the sanitized mount path,
	// and stdout preserves that byte-for-byte.
	argWithMount := filepath.Join(mountDir, "somefile.txt")
	if !strings.Contains(stdoutStr, "ARG:"+argWithMount) {
		t.Errorf("expected ARG output to reference mount path %q, got stdout: %q", argWithMount, stdoutStr)
	}

	// Verify env scrubbing
	if !strings.Contains(stdoutStr, "ENV_SCRUBBED:OK") {
		t.Errorf("expected ENV_SCRUBBED:OK, got stdout: %q", stdoutStr)
	}
}

func TestRunDaemonNotRunning(t *testing.T) {
	// Create temp home directory without starting a daemon.
	tmpHome, err := os.MkdirTemp("", "janusfs-test-home")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpHome) }()

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	exitCode, err := Run(context.Background(), []string{"echo", "hello"}, false)
	if exitCode != 125 {
		t.Errorf("expected exit code 125, got %d", exitCode)
	}
	if err == nil || !strings.Contains(err.Error(), "JanusFS daemon is not running") {
		t.Errorf("expected daemon not running error, got: %v", err)
	}
}

// TestFindSourceAndMountRefusesToGuess asserts that a cwd with no active mount
// and no .janusfs.yml ancestor is refused rather than silently
// mounted — defaulting to cwd would provision an unpoliced mount over whatever
// directory happens to be current (a user's entire home directory, in the
// worst case), which is the opposite of what this tool exists to prevent.
func TestFindSourceAndMountRefusesToGuess(t *testing.T) {
	tmpHome, err := os.MkdirTemp("", "janusfs-test-home")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpHome) }()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// A policy-free directory: no .janusfs.yml anywhere in its
	// ancestry within this isolated temp tree.
	policyFreeCwd, err := os.MkdirTemp("", "janusfs-nopolicy")
	if err != nil {
		t.Fatalf("failed to create cwd: %v", err)
	}
	defer func() { _ = os.RemoveAll(policyFreeCwd) }()
	policyFreeCwd, err = filepath.EvalSymlinks(policyFreeCwd)
	if err != nil {
		t.Fatalf("failed to resolve cwd: %v", err)
	}

	// A mock daemon that reports no active mounts, so the walk finds nothing
	// live to attach to either.
	sockPath := filepath.Join(tmpHome, ".janusfs", "daemon.sock")
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		t.Fatalf("failed to create socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen on socket: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req daemonRequest
			_ = json.NewDecoder(conn).Decode(&req)
			_ = json.NewEncoder(conn).Encode(daemonResponse{OK: true})
			_ = conn.Close()
		}
	}()

	_, _, err = findSourceAndMount(policyFreeCwd)
	if err == nil {
		t.Fatal("expected an error refusing to mount a policy-free directory, got nil")
	}
	if !strings.Contains(err.Error(), "no JanusFS policy found") {
		t.Errorf("expected a 'no JanusFS policy found' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "janusfs init") {
		t.Errorf("expected the error to name the remedy (janusfs init), got: %v", err)
	}
}
