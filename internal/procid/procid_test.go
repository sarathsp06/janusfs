package procid

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startChild spawns a long-lived subprocess that prints its PID and then
// sleeps. The test kills it via t.Cleanup. If env is non-nil it replaces
// the child's environment entirely; otherwise the parent's env is
// inherited. Returns the child's PID.
func startChild(t *testing.T, env []string, args ...string) int {
	t.Helper()
	if len(args) == 0 {
		args = []string{"sh", "-c", "echo $$; exec sleep 30"}
	}
	cmd := exec.Command(args[0], args[1:]...)
	if env != nil {
		cmd.Env = env
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, pipe)
		_, _ = cmd.Process.Wait()
	})
	type result struct {
		pid int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(pipe).ReadString('\n')
		if err != nil {
			ch <- result{err: err}
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		ch <- result{pid: pid, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read pid from child: %v", r.err)
		}
		return r.pid
	case <-time.After(3 * time.Second):
		t.Fatal("child did not print pid within 3s")
		return 0
	}
}

func TestStartTimeAndParentSelf(t *testing.T) {
	pid := os.Getpid()
	if _, err := startTime(pid); err != nil {
		t.Fatalf("startTime(self): %v", err)
	}
	p, err := parent(pid)
	if err != nil {
		t.Fatalf("parent(self): %v", err)
	}
	if p <= 0 {
		t.Errorf("parent(self) = %d, want > 0", p)
	}
}

func TestEnvironSelfReturnsSomething(t *testing.T) {
	// t.Setenv would not help here — on darwin, KERN_PROCARGS2 returns the
	// environ recorded at execve time, not the process's current runtime
	// environ. All this test can honestly assert is that environ(self)
	// succeeds and returns some non-empty block; anything set by the go
	// test runner (GOPATH, PATH, ...) should be present.
	env, err := environ(os.Getpid())
	if err != nil {
		t.Fatalf("environ(self): %v", err)
	}
	if len(env) == 0 {
		t.Error("environ(self) returned zero entries — expected the exec-time environ to be non-empty")
	}
}

func TestIsAgentUnregisteredPidIsNotAgent(t *testing.T) {
	r := NewMemRegistry()
	if r.IsAgent(os.Getpid()) {
		t.Fatal("expected an unregistered pid to be classified as host")
	}
	if s := r.Stats(); s.Host != 1 || s.Agent != 0 {
		t.Errorf("stats after one host lookup = %+v", s)
	}
}

func TestIsAgentDirectChildViaEnviron(t *testing.T) {
	// On darwin ≥ recent releases, the kernel omits the environ region
	// from KERN_PROCARGS2 for a cross-process read (see the caveat in
	// environ_darwin.go). Skip when environ scraping is not usable on the
	// current OS; the ancestry-walk path is exercised separately.
	env := append(os.Environ(), sessionEnvVar+"=probe-token")
	probePID := startChild(t, env)
	probeEnv, err := environ(probePID)
	if err != nil || len(probeEnv) == 0 {
		t.Skipf("environ(child) unavailable on this OS (err=%v, entries=%d) — environ path not testable", err, len(probeEnv))
	}
	// Also assert the probe actually saw JANUSFS_SESSION — if the kernel
	// truncated but returned something (unlikely but not impossible),
	// we cannot rely on this test's premise either.
	if _, ok := tokenFromEnviron(probeEnv); !ok {
		t.Skip("environ(child) returned entries but not JANUSFS_SESSION — environ region truncated by the kernel")
	}

	r := NewMemRegistry()
	token := "test-token-environ-primary"
	// Register a root pid that is deliberately NOT in the child's
	// ancestry, so only step 2 (environ) can pass.
	r.Register(token, Identity{PID: 999999, StartTime: 1})

	childEnv := append(os.Environ(), sessionEnvVar+"="+token)
	pid := startChild(t, childEnv)

	if !r.IsAgent(pid) {
		t.Errorf("expected child with JANUSFS_SESSION=%q to be classified as agent", token)
	}
}

func TestIsAgentAncestryWalk(t *testing.T) {
	// Child WITHOUT JANUSFS_SESSION so step 2 falls through; but its ppid
	// (this test process) is the registered root, so step 3 hits.
	r := NewMemRegistry()
	selfStart, err := startTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	r.Register("test-token-ancestry", Identity{PID: os.Getpid(), StartTime: selfStart})

	// Explicit env WITHOUT any JANUSFS_SESSION.
	scrubbed := scrubEnv(os.Environ(), sessionEnvVar)
	pid := startChild(t, scrubbed)

	if !r.IsAgent(pid) {
		t.Errorf("expected direct child of a registered root to be classified as agent")
	}
}

func TestIsAgentUnrelatedProcessIsHost(t *testing.T) {
	r := NewMemRegistry()
	// Register a completely different, definitely-not-us root.
	r.Register("test-token-unrelated", Identity{PID: 1234567, StartTime: 1})

	scrubbed := scrubEnv(os.Environ(), sessionEnvVar)
	pid := startChild(t, scrubbed)

	if r.IsAgent(pid) {
		t.Errorf("expected an unrelated child to be classified as host")
	}
}

// TestRecycledPidDoesNotInheritCachedVerdict fabricates a stale cache
// entry: the same pid, but the START TIME the entry was recorded under is
// different from what startTime(pid) now returns. The revalidation in
// step 1 must detect the mismatch, drop the entry, and re-classify from
// scratch — never serving the stale verdict.
func TestRecycledPidDoesNotInheritCachedVerdict(t *testing.T) {
	r := NewMemRegistry()
	pid := os.Getpid()
	realStart, err := startTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	// Poison the cache: record an "isAgent=true" verdict for this pid at a
	// deliberately-wrong start time.
	r.cache[pid] = cacheEntry{startTime: realStart - 12345, isAgent: true}

	if r.IsAgent(pid) {
		t.Fatalf("stale (pid, startTime) cache entry served as if fresh — PID reuse would leak an agent verdict")
	}
	// The stale entry must have been dropped and replaced with the
	// re-derived verdict.
	if e, ok := r.cache[pid]; !ok {
		t.Fatal("expected the cache to now hold a fresh entry for this pid")
	} else if e.startTime != realStart {
		t.Errorf("cache entry after reclassification has startTime=%d, want %d", e.startTime, realStart)
	}
}

func TestCacheHitCounter(t *testing.T) {
	r := NewMemRegistry()
	pid := os.Getpid()
	r.IsAgent(pid) // populates cache
	r.IsAgent(pid) // must be a cache hit
	if s := r.Stats(); s.CacheHits == 0 {
		t.Errorf("expected at least one cache hit, got stats %+v", s)
	}
}

func TestUnregisterDropsCache(t *testing.T) {
	r := NewMemRegistry()
	selfStart, err := startTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	tok := "test-token-unreg"
	r.Register(tok, Identity{PID: os.Getpid(), StartTime: selfStart})

	scrubbed := scrubEnv(os.Environ(), sessionEnvVar)
	pid := startChild(t, scrubbed)

	if !r.IsAgent(pid) {
		t.Fatal("expected agent verdict before Unregister")
	}
	r.Unregister(tok)
	if r.IsAgent(pid) {
		t.Errorf("expected host verdict after Unregister — the cache must have been dropped")
	}
}

func scrubEnv(env []string, name string) []string {
	pfx := name + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, pfx) {
			continue
		}
		out = append(out, e)
	}
	return out
}
