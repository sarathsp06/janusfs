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
	defer os.RemoveAll(tmpHome)

	// Save original home and restore later.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Define our source and mount directories.
	srcDir, err := os.MkdirTemp("", "janusfs-src")
	if err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}
	defer os.RemoveAll(srcDir)

	mountDir, err := os.MkdirTemp("", "janusfs-mount")
	if err != nil {
		t.Fatalf("failed to create mount dir: %v", err)
	}
	defer os.RemoveAll(mountDir)

	// Create a dummy file in source.
	dummyFile := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(dummyFile, []byte("hello from src"), 0o644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
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
	defer ln.Close()

	// Handle mock daemon requests in a goroutine.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
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
	defer os.Chdir(origCWD)

	// Set some environment variables.
	os.Setenv("JANUSFS_MOCK_SECRET", "supersecret")
	os.Setenv("MY_VAR", "myvalue")
	defer os.Unsetenv("JANUSFS_MOCK_SECRET")
	defer os.Unsetenv("MY_VAR")

	// Target command: compile a small Go program and execute it.
	helperGo := `
package main

import (
	"fmt"
	"os"
)

func main() {
	// Print current working directory
	cwd, _ := os.Getwd()
	fmt.Printf("CWD:%s\n", cwd)

	// Print arguments
	for _, arg := range os.Args[1:] {
		fmt.Printf("ARG:%s\n", arg)
	}

	// Print environment keys starting with JANUSFS_
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "JANUSFS_") {
			fmt.Println("LEAK:JANUSFS_MOCK_SECRET is not scrubbed")
			os.Exit(1)
		}
	}
	fmt.Println("ENV_SCRUBBED:OK")
}

import "strings"
`
	// Wait, the Go imports block should be at the top! Let's clean up the code.
	helperGo = `package main

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
	defer os.RemoveAll(testBinDir)

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
	exitCode, runErr := Run(context.Background(), []string{binFile, argWithSrc})

	// Restore stdout/stderr
	wOut.Close()
	wErr.Close()
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

	// Verify CWD hijacking: our compiled helper ran inside hijackedCWD which is mountDir.
	// BUT because of reverse output stream rewriting, any mention of mountDir in the output
	// should have been rewritten back to srcDir!
	if !strings.Contains(stdoutStr, "CWD:"+srcDir) {
		t.Errorf("expected CWD output to reference srcDir %q (via reverse translation), stdout: %q", srcDir, stdoutStr)
	}
	if strings.Contains(stdoutStr, mountDir) {
		t.Errorf("expected no references to mountDir %q in stdout, but found them: %q", mountDir, stdoutStr)
	}

	// Verify argument path translation
	if !strings.Contains(stdoutStr, "ARG:"+argWithSrc) {
		t.Errorf("expected ARG output to reference %q, got stdout: %q", argWithSrc, stdoutStr)
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
	defer os.RemoveAll(tmpHome)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	exitCode, err := Run(context.Background(), []string{"echo", "hello"})
	if exitCode != 125 {
		t.Errorf("expected exit code 125, got %d", exitCode)
	}
	if err == nil || !strings.Contains(err.Error(), "JanusFS daemon is not running") {
		t.Errorf("expected daemon not running error, got: %v", err)
	}
}
