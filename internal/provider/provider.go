// Package provider implements SPEC.md §8.3's RedactedContentProvider: the
// RAM cache of redacted bytes that internal/mount reads Masked files
// through. It is engine-agnostic (SPEC §5's dependency rule: nothing but
// mount/api depends on engine) — callers resolve a path's Decision and
// Patterns themselves (internal/engine) and pass the result in.
//
// ContentKey is the cache-validity key (FR-21: "every masked-file read
// validates (mtime, size, inode) against the cache key before serving" —
// the watcher is advisory only, this check is authoritative). A read whose
// key doesn't match the cached entry is treated as stale and triggers a
// rebuild (FR-20): concurrent readers are served the previous redacted
// bytes while the pattern set is unchanged, or block (bounded 10s, then
// EIO) otherwise. Oversized files (> --cache-max-file) bypass the cache
// entirely and stream-redact on every read (NFR-4).
package provider

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sarathsp06/janusfs/internal/apperrors"
	"github.com/sarathsp06/janusfs/internal/patterns"
	"github.com/sarathsp06/janusfs/internal/redact"
)

// rebuildTimeout is FR-20's bound: a concurrent reader waiting on a
// rebuild that hasn't produced any usable bytes yet gives up after this
// long and fails closed (EIO via apperrors.ErrRebuildTimeout), rather than
// blocking a FUSE handler forever on a wedged rebuild.
const rebuildTimeout = 10 * time.Second

// ContentKey identifies exactly one version of one file's redacted
// content (SPEC §8.3). Path is the absolute real path on disk. Two reads
// with an identical key are guaranteed to want the same redacted bytes;
// any field differing means the cache entry (if any) is stale.
type ContentKey struct {
	Path    string
	MTimeNS int64
	Size    int64
	Inode   uint64
	Gen     uint64
}

// ProviderStats is a point-in-time snapshot for janusfs doctor / the future
// dashboard (FR-23).
type ProviderStats struct {
	Entries  int
	Bytes    int64
	Hits     uint64
	Misses   uint64
	Rebuilds uint64
}

// RedactedContentProvider serves redacted bytes for a Masked file (SPEC
// §8.3). ReadAt takes the caller-resolved pattern set directly (rather
// than looking it up itself) since this package has no engine dependency.
type RedactedContentProvider interface {
	ReadAt(ctx context.Context, key ContentKey, pats []*patterns.Pattern, p []byte, off int64) (int, error)
	Invalidate(path string)
	InvalidateAll()
	Stats() ProviderStats
}

// entry is one cached file's redacted bytes plus the key/pattern-set they
// were built against. Once built is true, bytes/rebuildErr are immutable
// and safe to read without the cache's lock (only the rebuild goroutine
// ever writes them, exactly once, before setting built).
type entry struct {
	key        ContentKey
	patternSig string // a stable signature of the pattern set the bytes were built from (FR-20's "pattern set unchanged" check)
	ready      chan struct{}
	built      bool // guarded by RamCache.mu; true once bytes/rebuildErr are final
	bytes      []byte
	rebuildErr error
	lruElem    *list.Element
}

// RamCache implements RedactedContentProvider as a single-mutex-guarded LRU
// (SPEC §8.3 calls for "sharded"; a single mutex is this MVP's
// simplification — the lock is only ever held for map/list bookkeeping,
// never across a rebuild or a redact call, so contention is limited to
// that bookkeeping, not the expensive work).
//
// ponytail: single mutex, not literally sharded — upgrade to N-way sharding
// by path hash if profiling ever shows map-lock contention under real
// concurrent load; NFR-5's actual requirement (no FUSE handler blocks on
// another's rebuild) is already satisfied since rebuilds run outside this
// lock.
type RamCache struct {
	mu       sync.Mutex
	entries  map[string]*entry // keyed by ContentKey.Path
	lru      *list.List        // most-recently-used at the front
	curBytes int64

	maxBytes        int64
	maxFile         int64
	redactBufferMax int64

	hits, misses, rebuilds atomic.Uint64
}

// NewRamCache constructs a RamCache. maxBytes/maxFile are --cache-max-bytes
// /--cache-max-file (NFR-4); redactBufferMax is --redact-buffer-max (§8.2),
// used by the oversize-bypass streaming path.
func NewRamCache(maxBytes, maxFile, redactBufferMax int64) *RamCache {
	return &RamCache{
		entries:         make(map[string]*entry),
		lru:             list.New(),
		maxBytes:        maxBytes,
		maxFile:         maxFile,
		redactBufferMax: redactBufferMax,
	}
}

// ReadAt implements RedactedContentProvider.ReadAt.
func (c *RamCache) ReadAt(ctx context.Context, key ContentKey, pats []*patterns.Pattern, p []byte, off int64) (int, error) {
	if key.Size > c.maxFile {
		return c.readOversize(key, pats, p, off)
	}

	sig := patternSignature(pats)

	c.mu.Lock()
	if e, ok := c.entries[key.Path]; ok && e.key == key {
		// Exact key match: either already ready, or a rebuild for this
		// exact version is in flight (another reader triggered it) —
		// either way, wait on it below rather than starting a duplicate
		// rebuild (singleflight per path).
		c.touchLocked(e)
		c.mu.Unlock()
		return c.waitAndServe(ctx, e, sig, p, off)
	}

	// Stale or absent. Detach any existing (stale) entry from bookkeeping
	// now — its bytes are superseded by the incoming rebuild either way —
	// but keep a reference if its pattern set is unchanged (FR-20: serve
	// those stale-but-still-valid bytes to this caller immediately while
	// the rebuild runs for the next one).
	var stalePrev *entry
	if e, ok := c.entries[key.Path]; ok {
		c.lru.Remove(e.lruElem)
		if e.built {
			c.curBytes -= int64(len(e.bytes))
		}
		if e.built && e.patternSig == sig {
			stalePrev = e
		}
	}
	c.misses.Add(1)
	c.rebuilds.Add(1)
	ne := &entry{key: key, patternSig: sig, ready: make(chan struct{})}
	c.entries[key.Path] = ne
	ne.lruElem = c.lru.PushFront(ne)
	c.mu.Unlock()

	go c.rebuild(ne, pats)

	if stalePrev != nil {
		return copyAt(stalePrev.bytes, p, off), nil
	}
	return c.waitAndServe(ctx, ne, sig, p, off)
}

// rebuild reads and redacts key.Path's real content, then publishes the
// result (or error) by closing ready. Runs without holding c.mu: NFR-5
// requires no FUSE handler block on a rebuild in progress, only on this
// specific entry's own completion (waitAndServe).
func (c *RamCache) rebuild(e *entry, pats []*patterns.Pattern) {
	bytesOut, err := redactFile(e.key, pats)

	c.mu.Lock()
	e.rebuildErr = err
	if err == nil {
		e.bytes = bytesOut
		c.curBytes += int64(len(bytesOut))
	}
	e.built = true
	c.evictLocked()
	c.mu.Unlock()
	close(e.ready)
}

// waitAndServe blocks until e's rebuild completes (bounded by
// rebuildTimeout, FR-20) and copies the requested range into p.
func (c *RamCache) waitAndServe(ctx context.Context, e *entry, sig string, p []byte, off int64) (int, error) {
	select {
	case <-e.ready:
		if e.rebuildErr != nil {
			return 0, e.rebuildErr
		}
		if e.patternSig != sig {
			// The entry that just became ready belongs to a different
			// pattern set than what this caller needs (a race between two
			// generations) — fail closed rather than serve mismatched
			// content.
			return 0, fmt.Errorf("provider: pattern set changed mid-rebuild for %q: %w", e.key.Path, apperrors.ErrRebuildTimeout)
		}
		return copyAt(e.bytes, p, off), nil
	case <-time.After(rebuildTimeout):
		return 0, fmt.Errorf("provider: rebuilding %q: %w", e.key.Path, apperrors.ErrRebuildTimeout)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// readOversize implements NFR-4's "single cached file > --cache-max-file is
// refused; masked reads for such files use on-the-fly streaming redaction
// per read." It redacts from the start of the file through off+len(p)
// (the minimum needed to correctly resolve any match that might start
// before off) and copies out the requested range.
//
// ponytail: reprocesses from byte 0 on every read rather than maintaining a
// resumable streaming cursor, so cost grows with off — acceptable per
// NFR-4's own "still correct, slower" framing for this rare oversize case;
// upgrade path is Phase 6's tmpfs shadow provider, which SPEC.md already
// plans to replace this bypass with.
func (c *RamCache) readOversize(key ContentKey, pats []*patterns.Pattern, p []byte, off int64) (int, error) {
	f, err := os.Open(key.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	need := off + int64(len(p))
	var out bytesSink
	if err := redact.Stream(&out, io.LimitReader(f, need), pats, c.redactBufferMax); err != nil {
		return 0, fmt.Errorf("provider: streaming redact %q: %w", key.Path, apperrors.ErrRedactUnsupported)
	}
	return copyAt(out.b, p, off), nil
}

// Invalidate drops path's cache entry (if any), zeroing its bytes first
// (NFR-1 best-effort). Called on a data-change event (FR-20).
func (c *RamCache) Invalidate(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked(path)
}

func (c *RamCache) invalidateLocked(path string) {
	e, ok := c.entries[path]
	if !ok {
		return
	}
	delete(c.entries, path)
	c.lru.Remove(e.lruElem)
	if e.built {
		c.curBytes -= int64(len(e.bytes))
		zero(e.bytes)
	}
}

// InvalidateAll drops every cached entry (FR-19: a generation swap
// conservatively invalidates everything; also used on watcher overflow).
func (c *RamCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.entries {
		c.invalidateLocked(path)
	}
}

// Stats returns a point-in-time snapshot (FR-23).
func (c *RamCache) Stats() ProviderStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ProviderStats{
		Entries:  len(c.entries),
		Bytes:    c.curBytes,
		Hits:     c.hits.Load(),
		Misses:   c.misses.Load(),
		Rebuilds: c.rebuilds.Load(),
	}
}

func (c *RamCache) touchLocked(e *entry) {
	c.hits.Add(1)
	c.lru.MoveToFront(e.lruElem)
}

// evictLocked drops least-recently-used, already-built entries until
// curBytes fits within maxBytes, zeroing each entry's bytes before
// releasing it (NFR-1 best-effort). Called with c.mu held. An entry still
// being built (not yet e.built) is never evicted — its rebuild goroutine
// owns it until completion — so eviction may transiently leave curBytes
// over budget while builds are in flight; it catches up on the next call.
func (c *RamCache) evictLocked() {
	for elem := c.lru.Back(); c.curBytes > c.maxBytes && elem != nil; {
		e := elem.Value.(*entry)
		prev := elem.Prev()
		if e.built {
			delete(c.entries, e.key.Path)
			c.lru.Remove(elem)
			c.curBytes -= int64(len(e.bytes))
			zero(e.bytes)
		}
		elem = prev
	}
}

func redactFile(key ContentKey, pats []*patterns.Pattern) ([]byte, error) {
	f, err := os.Open(key.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return redact.Redact(buf, pats), nil
}

// patternSignature gives a pattern set a stable, order-independent identity
// string for FR-20's "pattern set unchanged" comparison. Pattern.Name is
// unique per builtin and per distinct custom regex source (SPEC §13's
// reserved-name rule), so a sorted join of names is a sufficient signature.
func patternSignature(pats []*patterns.Pattern) string {
	names := make([]string, len(pats))
	for i, p := range pats {
		names[i] = p.Name
	}
	sort.Strings(names)
	sig := ""
	for _, n := range names {
		sig += n + "\x00"
	}
	return sig
}

func copyAt(src, dst []byte, off int64) int {
	if off >= int64(len(src)) {
		return 0
	}
	return copy(dst, src[off:])
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// bytesSink is a minimal io.Writer accumulating into a byte slice, used by
// readOversize to capture redact.Stream's output.
type bytesSink struct{ b []byte }

func (w *bytesSink) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
