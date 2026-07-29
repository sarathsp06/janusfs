//go:build linux && fuseintegration

// NFR-11 benchmark: host tools must show NO measurable regression while a
// janusfs exec session is active, because they never touch FUSE at all under
// the namespace model — "reduced overhead" is not the target, zero is.
//
// NOTE: same caveat as isolation_linux_test.go — authored against documented
// Linux semantics, not executed on a real Linux machine in this change.
// Run on an actual Linux box before treating its numbers as a real NFR-11
// gate result.
package execrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// benchReadFile is the thing under measurement: a plain, direct host-side
// sequential read of one file — never touching the mount, since under the
// namespace model there is nothing FUSE-related for a host process to touch.
func benchReadFile(b *testing.B, path string) {
	b.Helper()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := os.ReadFile(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHostRead_NoActiveExecSession is the baseline: no exec session
// running at all.
func BenchmarkHostRead_NoActiveExecSession(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("Linux-only")
	}
	dir := b.TempDir()
	path := filepath.Join(dir, "data.txt")
	// A few KB — representative of a real source file, not a synthetic
	// worst case; NFR-3's redaction throughput target already covers large
	// files, this benchmark is about per-call overhead, not raw throughput.
	if err := os.WriteFile(path, make([]byte, 8192), 0o644); err != nil {
		b.Fatal(err)
	}
	benchReadFile(b, path)
}

// BenchmarkHostRead_WithActiveExecSession runs the identical read while a
// `janusfs exec` session is concurrently active in the background over a
// SEPARATE source tree, so any measurable difference from the baseline above
// would indicate the exec session imposed some global cost on host I/O —
// which the namespace model's whole premise says should not happen, since a
// host process never enters FUSE regardless of what namespace some other
// process is running in.
func BenchmarkHostRead_WithActiveExecSession(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("Linux-only")
	}

	// Build the real binary once; needed because the exec session re-execs
	// itself as `janusfs __nsmount`.
	dirBin := b.TempDir()
	bin := filepath.Join(dirBin, "janusfs")
	wd, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/janusfs")
	buildCmd.Dir = filepath.Join(wd, "..", "..")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		b.Fatalf("building janusfs binary: %v\n%s", err, out)
	}

	execSrc := b.TempDir()
	if err := os.WriteFile(filepath.Join(execSrc, ".janusfs.yml"), []byte("version: 1\nhide:\n  - \"*.secret\"\n"), 0o644); err != nil {
		b.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A long-lived background exec session: `sleep` for the duration of the
	// benchmark, so its namespace and FUSE server stay alive concurrently
	// with the host-side reads being measured below.
	sessionCmd := exec.CommandContext(ctx, bin, "exec", "--", "sleep", "300")
	sessionCmd.Dir = execSrc
	if err := sessionCmd.Start(); err != nil {
		b.Fatalf("starting background exec session: %v", err)
	}
	defer func() {
		cancel()
		_ = sessionCmd.Wait()
	}()
	time.Sleep(500 * time.Millisecond) // let the namespace/mount come up

	readDir := b.TempDir()
	path := filepath.Join(readDir, "data.txt")
	if err := os.WriteFile(path, make([]byte, 8192), 0o644); err != nil {
		b.Fatal(err)
	}
	benchReadFile(b, path)
}
