package procid

import (
	"os"
	"testing"
)

// BenchmarkStartTime measures the raw platform read: on darwin one
// KERN_PROC_PID sysctl, on linux one open+read+parse of /proc/<pid>/stat.
// This is the syscall cost the IsAgent cache-hit path pays on every
// revalidation. Recorded in bench/BASELINE.md — if it does not sit inside
// a small fraction of NFR-3's 250 µs p99 budget, PRP 06 is declined.
func BenchmarkStartTime(b *testing.B) {
	pid := os.Getpid()
	if _, err := startTime(pid); err != nil {
		b.Fatalf("startTime warmup: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := startTime(pid); err != nil {
			b.Fatalf("startTime: %v", err)
		}
	}
}

// BenchmarkIsAgentCacheHit stands in for the cache-hit path that will be
// added in Task 2: a memoized verdict guarded by one startTime revalidation.
// Until Registry lands, this measures the revalidation cost alone — the
// only per-call syscall the cache-hit path performs — plus a map lookup
// against a pre-populated map keyed by (pid, startTime), which is the shape
// the real cache will take.
func BenchmarkIsAgentCacheHit(b *testing.B) {
	pid := os.Getpid()
	st, err := startTime(pid)
	if err != nil {
		b.Fatalf("startTime warmup: %v", err)
	}
	cache := map[Identity]bool{{PID: pid, StartTime: st}: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cur, err := startTime(pid)
		if err != nil {
			b.Fatalf("startTime: %v", err)
		}
		if _, ok := cache[Identity{PID: pid, StartTime: cur}]; !ok {
			b.Fatal("cache miss on same-process revalidation")
		}
	}
}

// BenchmarkAncestryWalk simulates the cold path: walk parent() up to depth
// 5, which is roughly how deep an "agent shell → tool → subprocess"
// ancestry sits under a session root. This is the worst per-operation
// cost before the verdict is cached.
func BenchmarkAncestryWalk(b *testing.B) {
	pid := os.Getpid()
	if _, err := parent(pid); err != nil {
		b.Fatalf("parent warmup: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cur := pid
		for depth := 0; depth < 5; depth++ {
			p, err := parent(cur)
			if err != nil || p <= 1 {
				break
			}
			cur = p
		}
	}
}
