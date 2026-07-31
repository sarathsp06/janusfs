package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

func opener(path string) Opener {
	return func() (io.ReadCloser, error) { return os.Open(path) }
}

func writeFile(t *testing.T, path, content string) ContentKey {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var inode uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		inode = st.Ino
	}
	return NewContentKey(path, fi.ModTime().UnixNano(), fi.Size(), inode, 1)
}

func envValuePats(t *testing.T) []*patterns.Pattern {
	t.Helper()
	ps, err := patterns.ParsePatternRef("env-value")
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestReadAtBasicHitAndMiss(t *testing.T) {
	dir := t.TempDir()
	key := writeFile(t, filepath.Join(dir, ".env"), "API_KEY=supersecret\n")
	pats := envValuePats(t)

	c := NewRamCache(1<<20, 1<<20, 1<<20)
	p := make([]byte, 64)
	n, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path()))
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	got := string(p[:n])
	if got != "API_KEY=***********\n" {
		t.Fatalf("got %q", got)
	}

	stats := c.Stats()
	if stats.Misses != 1 || stats.Hits != 0 {
		t.Fatalf("stats after first read = %+v, want 1 miss 0 hits", stats)
	}

	// Second read with the same key is a cache hit.
	n2, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path()))
	if err != nil {
		t.Fatalf("ReadAt (2nd): %v", err)
	}
	if string(p[:n2]) != got {
		t.Fatalf("second read diverged: %q vs %q", p[:n2], got)
	}
	stats = c.Stats()
	if stats.Hits != 1 {
		t.Fatalf("stats after second read = %+v, want 1 hit", stats)
	}
}

func TestReadAtOffset(t *testing.T) {
	dir := t.TempDir()
	key := writeFile(t, filepath.Join(dir, ".env"), "API_KEY=supersecret\nDEBUG=true\n")
	pats := envValuePats(t)

	c := NewRamCache(1<<20, 1<<20, 1<<20)
	p := make([]byte, 5)
	n, err := c.ReadAt(context.Background(), key, pats, p, int64(len("API_KEY=***********\n")), opener(key.Path()))
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(p[:n]) != "DEBUG" {
		t.Fatalf("offset read = %q, want %q", p[:n], "DEBUG")
	}
}

func TestReadAtStaleKeyServesStaleThenRebuilds(t *testing.T) {
	// A read that finds its key stale is served the
	// *previous* redacted bytes immediately (if the pattern set is
	// unchanged) while a rebuild runs in the background; only a
	// subsequent read observes the rebuilt content.
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	key1 := writeFile(t, path, "API_KEY=firstvalue12\n")
	pats := envValuePats(t)

	c := NewRamCache(1<<20, 1<<20, 1<<20)
	p := make([]byte, 64)
	if _, err := c.ReadAt(context.Background(), key1, pats, p, 0, opener(key1.Path())); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}

	// Simulate a content change: new mtime/size, same pattern set.
	time.Sleep(2 * time.Millisecond)
	key2 := writeFile(t, path, "API_KEY=secondvalueaaaa\n")
	if key2 == key1 {
		t.Fatal("test setup: expected key to change after rewrite")
	}

	n, err := c.ReadAt(context.Background(), key2, pats, p, 0, opener(key2.Path()))
	if err != nil {
		t.Fatalf("ReadAt after change: %v", err)
	}
	if got := string(p[:n]); got != "API_KEY=************\n" {
		t.Fatalf("expected the immediate read to be served the stale (pre-change) bytes, got %q", got)
	}

	// Give the background rebuild a moment to complete, then a fresh read
	// for the same (now-cached) key must reflect the new content.
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := c.ReadAt(context.Background(), key2, pats, p, 0, opener(key2.Path()))
		if err != nil {
			t.Fatalf("ReadAt (waiting for rebuild): %v", err)
		}
		if got := string(p[:n]); got == "API_KEY=***************\n" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background rebuild did not complete in time; last read = %q", p[:n])
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReadAtOversizeBypassesCache(t *testing.T) {
	dir := t.TempDir()
	// maxFile smaller than the actual file forces the oversize path.
	content := "API_KEY=abovethelimit1\n"
	key := writeFile(t, filepath.Join(dir, ".env"), content)
	pats := envValuePats(t)

	c := NewRamCache(1<<20, 4 /* tiny maxFile */, 1<<20)
	p := make([]byte, 64)
	n, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path()))
	if err != nil {
		t.Fatalf("ReadAt (oversize): %v", err)
	}
	got := string(p[:n])
	if got != "API_KEY=**************\n" {
		t.Fatalf("got %q", got)
	}
	if stats := c.Stats(); stats.Entries != 0 {
		t.Fatalf("oversize read must not populate the cache, got %+v", stats)
	}
}

func TestInvalidateDropsEntry(t *testing.T) {
	dir := t.TempDir()
	key := writeFile(t, filepath.Join(dir, ".env"), "API_KEY=value12345678\n")
	pats := envValuePats(t)

	c := NewRamCache(1<<20, 1<<20, 1<<20)
	p := make([]byte, 64)
	if _, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path())); err != nil {
		t.Fatal(err)
	}
	if stats := c.Stats(); stats.Entries != 1 {
		t.Fatalf("expected 1 cached entry, got %+v", stats)
	}
	c.Invalidate(key.Path())
	if stats := c.Stats(); stats.Entries != 0 {
		t.Fatalf("expected entry removed after Invalidate, got %+v", stats)
	}
}

func TestInvalidateAllDropsEverything(t *testing.T) {
	dir := t.TempDir()
	pats := envValuePats(t)
	c := NewRamCache(1<<20, 1<<20, 1<<20)

	for i := 0; i < 5; i++ {
		key := writeFile(t, filepath.Join(dir, string(rune('a'+i))+".env"), "API_KEY=value12345678\n")
		p := make([]byte, 64)
		if _, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path())); err != nil {
			t.Fatal(err)
		}
	}
	if stats := c.Stats(); stats.Entries != 5 {
		t.Fatalf("expected 5 entries, got %+v", stats)
	}
	c.InvalidateAll()
	if stats := c.Stats(); stats.Entries != 0 || stats.Bytes != 0 {
		t.Fatalf("expected empty cache after InvalidateAll, got %+v", stats)
	}
}

func TestEvictionRespectsMaxBytes(t *testing.T) {
	dir := t.TempDir()
	pats := envValuePats(t)
	content := "API_KEY=" + string(make([]byte, 200)) + "\n" // ~208 bytes each

	// Budget small enough that only ~2 entries fit.
	c := NewRamCache(500, 1<<20, 1<<20)
	var lastKey ContentKey
	for i := 0; i < 5; i++ {
		key := writeFile(t, filepath.Join(dir, string(rune('a'+i))+".env"), content)
		p := make([]byte, len(content))
		if _, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path())); err != nil {
			t.Fatal(err)
		}
		lastKey = key
	}
	stats := c.Stats()
	if stats.Bytes > 500 {
		t.Fatalf("curBytes = %d, want <= 500 (maxBytes)", stats.Bytes)
	}
	if stats.Entries >= 5 {
		t.Fatalf("expected eviction to have dropped some entries, got %d of 5", stats.Entries)
	}
	// The most recently used entry must have survived eviction (LRU evicts
	// from the back, not the just-inserted front).
	p := make([]byte, len(content))
	n, err := c.ReadAt(context.Background(), lastKey, pats, p, 0, opener(lastKey.Path()))
	if err != nil {
		t.Fatalf("ReadAt for most-recent entry: %v", err)
	}
	if n == 0 {
		t.Fatal("expected non-empty read for most-recently-used entry")
	}
}

// TestConcurrentReadsSinglePathNoRace exercises many goroutines hitting the
// same path concurrently, mixing initial-population races with steady-state
// hits — meant to be run under -race.
func TestConcurrentReadsSinglePathNoRace(t *testing.T) {
	dir := t.TempDir()
	key := writeFile(t, filepath.Join(dir, ".env"), "API_KEY=concurrentvalue12\n")
	pats := envValuePats(t)
	c := NewRamCache(1<<20, 1<<20, 1<<20)

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := make([]byte, 64)
			n, err := c.ReadAt(context.Background(), key, pats, p, 0, opener(key.Path()))
			if err != nil {
				errs <- err
				return
			}
			if got := string(p[:n]); got != "API_KEY=*****************\n" {
				errs <- fmt.Errorf("unexpected redacted content: %q", got)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkPatternSignature_Zero(b *testing.B) {
	var pats []*patterns.Pattern
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = patternSignature(pats)
	}
}

func BenchmarkPatternSignature_One(b *testing.B) {
	pats := []*patterns.Pattern{{Name: "env-value"}}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = patternSignature(pats)
	}
}

func BenchmarkPatternSignature_Three(b *testing.B) {
	pats := []*patterns.Pattern{
		{Name: "env-value"},
		{Name: "aws-key"},
		{Name: "jwt"},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = patternSignature(pats)
	}
}

func BenchmarkPatternSignature_Eight(b *testing.B) {
	pats := []*patterns.Pattern{
		{Name: "env-value"},
		{Name: "aws-key"},
		{Name: "jwt"},
		{Name: "db-uri"},
		{Name: "github-token"},
		{Name: "generic-secret"},
		{Name: "private-key"},
		{Name: "custom-regex"},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = patternSignature(pats)
	}
}
