// Package provider is the RAM cache of redacted bytes that internal/mount reads
// Masked files through. It deliberately does not import internal/engine:
// callers resolve a path's Decision and pattern set themselves and pass the
// result in, which keeps this cache testable without a rule tree and stops it
// from becoming a second policy-resolution path.
//
// ContentKey is the cache-validity key. Every masked read re-stats the real
// file and validates (mtime, size, inode, generation) before serving, which
// makes this check the authoritative change detector — there is no file
// watcher. A read whose
// rebuild: concurrent readers are served the previous redacted
// bytes while the pattern set is unchanged, or block (bounded 10s, then
// EIO) otherwise. Oversized files (> --cache-max-file) bypass the cache
// entirely and stream-redact on every read.
package provider

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sarathsp06/janusfs/internal/apperrors"
	"github.com/sarathsp06/janusfs/internal/patterns"
	"github.com/sarathsp06/janusfs/internal/redact"
)

// rebuildTimeout bounds how long a concurrent reader waits on a
// rebuild that hasn't produced any usable bytes yet: it gives up after this
// long and fails closed (EIO via apperrors.ErrRebuildTimeout), rather than
// blocking a FUSE handler forever on a wedged rebuild.
const rebuildTimeout = 10 * time.Second

// ContentKey identifies exactly one version of one file's redacted content.
// Two reads with an identical key are guaranteed to want the same redacted
// bytes; any field differing means the cache entry (if any) is stale — this
// whole-struct equality (see ReadAt) is the authoritative change detector.
//
// Its fields are unexported and it can only be built through NewContentKey, so
// the freshness contract lives here next to the equality check: a caller
// cannot silently omit a field (e.g. the generation) and defeat staleness
// detection, which — since the caller lives in another package (internal/mount)
// with no compile-time tie to this equality — is exactly the bug that could
// otherwise hide across the seam.
type ContentKey struct {
	path    string // absolute real path on disk; identity/map key ONLY, never an access path
	mtimeNS int64
	size    int64
	inode   uint64
	gen     uint64
}

// NewContentKey builds the cache-validity key from the freshness inputs a
// masked read has on hand: the file's identity path, its modification time in
// whole nanoseconds, size, inode, and the rule-set generation. Requiring every
// field as an argument is the point — there is no way to construct a key that
// omits one.
func NewContentKey(path string, mtimeNS, size int64, inode, gen uint64) ContentKey {
	return ContentKey{path: path, mtimeNS: mtimeNS, size: size, inode: inode, gen: gen}
}

// Path returns the identity path this key was built for. It is the cache map
// key, not an access path — production callers read the file through
// internal/backing, never by reopening this string.
func (k ContentKey) Path() string { return k.path }

// Opener returns a fresh, independently readable handle on the content a
// ContentKey identifies. Callers (internal/mount) back this with a
// descriptor-relative open (internal/backing) rather than re-resolving
// ContentKey.path as a string, so the decision that produced this key and the
// bytes actually read for it go through the same resolution — closing the
// window a path re-open would leave between the two. This package calls
// Opener exactly once per rebuild (or once per readOversize call), always
// closing what it returns.
type Opener func() (io.ReadCloser, error)

// ProviderStats is a point-in-time snapshot for janusfs doctor / the future
// dashboard.
type ProviderStats struct {
	Entries  int
	Bytes    int64
	Hits     uint64
	Misses   uint64
	Rebuilds uint64
}

// RedactedContentProvider serves redacted bytes for a Masked file. ReadAt takes
// the caller-resolved pattern set directly rather than looking it up itself,
// since this package has no engine dependency. open is called at most once,
// only on a genuine cache miss or oversize bypass — never on a cache hit.
type RedactedContentProvider interface {
	ReadAt(ctx context.Context, key ContentKey, pats []*patterns.Pattern, p []byte, off int64, open Opener) (int, error)
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
	patternSig string // stable signature of the pattern set the bytes were built from
	ready      chan struct{}
	built      bool // guarded by RamCache.mu; true once bytes/rebuildErr are final
	bytes      []byte
	rebuildErr error
	lruElem    *list.Element
}

// RamCache implements RedactedContentProvider as a single-mutex-guarded LRU.
// The lock is only ever held for map and list bookkeeping, never across a
// rebuild or a redact call, so contention is limited to that bookkeeping rather
// than the expensive work.
//
// ponytail: single mutex, not literally sharded — upgrade to N-way sharding
// by path hash if profiling ever shows map-lock contention under real
// concurrent load. The property that actually matters — no FUSE handler blocks
// on another's rebuild — is already satisfied, since rebuilds run outside this
// lock.
type RamCache struct {
	mu       sync.Mutex
	entries  map[string]*entry // keyed by ContentKey.path
	lru      *list.List        // most-recently-used at the front
	curBytes int64

	maxBytes        int64
	maxFile         int64
	redactBufferMax int64

	hits, misses, rebuilds atomic.Uint64
}

// NewRamCache constructs a RamCache. maxBytes/maxFile are --cache-max-bytes
// /--cache-max-file; redactBufferMax is --redact-buffer-max,
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
func (c *RamCache) ReadAt(ctx context.Context, key ContentKey, pats []*patterns.Pattern, p []byte, off int64, open Opener) (int, error) {
	if key.size > c.maxFile {
		return c.readOversize(pats, p, off, open)
	}

	sig := patternSignature(pats)

	c.mu.Lock()
	if e, ok := c.entries[key.path]; ok && e.key == key {
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
	// but keep a reference if its pattern set is unchanged, so those
	// stale-but-still-redacted bytes can be served to this caller immediately
	// while the rebuild runs for the next one.
	var stalePrev *entry
	if e, ok := c.entries[key.path]; ok {
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
	c.entries[key.path] = ne
	ne.lruElem = c.lru.PushFront(ne)
	c.mu.Unlock()

	go c.rebuild(ne, pats, open)

	if stalePrev != nil {
		return copyAt(stalePrev.bytes, p, off), nil
	}
	return c.waitAndServe(ctx, ne, sig, p, off)
}

// rebuild reads and redacts the real content open provides, then publishes
// the result (or error) by closing ready. Runs without holding c.mu, so no
// FUSE handler blocks on a rebuild in progress — only on this specific
// entry's own completion, in waitAndServe.
func (c *RamCache) rebuild(e *entry, pats []*patterns.Pattern, open Opener) {
	bytesOut, err := redactFile(open, pats)

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

// waitAndServe blocks until e's rebuild completes, bounded by rebuildTimeout,
// and copies the requested range into p.
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
			return 0, fmt.Errorf("provider: pattern set changed mid-rebuild for %q: %w", e.key.path, apperrors.ErrRebuildTimeout)
		}
		return copyAt(e.bytes, p, off), nil
	case <-time.After(rebuildTimeout):
		return 0, fmt.Errorf("provider: rebuilding %q: %w", e.key.path, apperrors.ErrRebuildTimeout)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// readOversize handles a file larger than --cache-max-file, which is refused
// from the cache entirely and stream-redacted on every read instead. It redacts
// from the start of the file through off+len(p)
// (the minimum needed to correctly resolve any match that might start
// before off) and copies out the requested range.
//
// ponytail: reprocesses from byte 0 on every read rather than maintaining a
// resumable streaming cursor, so cost grows with off. Acceptable because
// oversize masked files are rare and the result is still correct, just slower;
// the upgrade path is a tmpfs shadow provider that replaces this bypass.
func (c *RamCache) readOversize(pats []*patterns.Pattern, p []byte, off int64, open Opener) (int, error) {
	f, err := open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	need := off + int64(len(p))
	var out bytesSink
	if err := redact.Stream(&out, io.LimitReader(f, need), pats, c.redactBufferMax); err != nil {
		return 0, fmt.Errorf("provider: streaming redact: %w", apperrors.ErrRedactUnsupported)
	}
	return copyAt(out.b, p, off), nil
}

// Invalidate drops path's cache entry (if any), zeroing its bytes first. The
// zeroing is best-effort: Go's GC may already have copied the slice.
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

// InvalidateAll drops every cached entry. Called on a rule reload, which
// conservatively invalidates everything rather than working out which entries a
// generation swap actually affected.
func (c *RamCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.entries {
		c.invalidateLocked(path)
	}
}

// Stats returns a point-in-time snapshot.
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
// releasing it, best-effort. Called with c.mu held. An entry still
// being built (not yet e.built) is never evicted — its rebuild goroutine
// owns it until completion — so eviction may transiently leave curBytes
// over budget while builds are in flight; it catches up on the next call.
func (c *RamCache) evictLocked() {
	for elem := c.lru.Back(); c.curBytes > c.maxBytes && elem != nil; {
		e := elem.Value.(*entry)
		prev := elem.Prev()
		if e.built {
			delete(c.entries, e.key.path)
			c.lru.Remove(elem)
			c.curBytes -= int64(len(e.bytes))
			zero(e.bytes)
		}
		elem = prev
	}
}

func redactFile(open Opener, pats []*patterns.Pattern) ([]byte, error) {
	f, err := open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return redact.Redact(buf, pats), nil
}

// patternSignature gives a pattern set a stable, order-independent identity
// string, for deciding whether a cached entry's pattern set is unchanged.
// Pattern.Name is unique per builtin and per distinct custom regex source, so a
// sorted join of names is a sufficient signature.
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
