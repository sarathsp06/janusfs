package obs

import (
	"sort"
	"sync"
)

// TopN tracks the most-served paths by read count and byte count
// (SPEC §10: "bounded count-min-sketch-backed LRU (track 1000 paths,
// report top 50 by reads and by bytes)").
type TopN struct {
	mu         sync.Mutex
	byReads    map[string]uint64
	byBytes    map[string]int64
	maxTracked int
}

// NewTopN creates a TopN tracker. maxTracked is the number of paths to
// track before evicting least-active entries.
func NewTopN(maxTracked int) *TopN {
	return &TopN{
		byReads:    make(map[string]uint64),
		byBytes:    make(map[string]int64),
		maxTracked: maxTracked,
	}
}

// Record increments the read and byte counters for a path.
func (t *TopN) Record(path string, bytes int64) {
	t.mu.Lock()
	t.byReads[path]++
	t.byBytes[path] += bytes
	if len(t.byReads) > t.maxTracked {
		t.evictOne()
	}
	t.mu.Unlock()
}

// evictOne removes the entry with the lowest read count.
func (t *TopN) evictOne() {
	var minPath string
	var minVal uint64 = 1<<64 - 1
	for p, v := range t.byReads {
		if v < minVal {
			minVal = v
			minPath = p
		}
	}
	delete(t.byReads, minPath)
	delete(t.byBytes, minPath)
}

// TopReads returns the top n paths by read count, sorted descending.
type TopEntry struct {
	Path  string `json:"path"`
	Reads uint64 `json:"reads"`
	Bytes int64  `json:"bytes"`
}

func (t *TopN) TopReads(n int) []TopEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := make([]TopEntry, 0, len(t.byReads))
	for p, r := range t.byReads {
		entries = append(entries, TopEntry{Path: p, Reads: r, Bytes: t.byBytes[p]})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Reads > entries[j].Reads
	})
	if n > 0 && n < len(entries) {
		entries = entries[:n]
	}
	return entries
}

// TopBytes returns the top n paths by byte count, sorted descending.
func (t *TopN) TopBytes(n int) []TopEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := make([]TopEntry, 0, len(t.byBytes))
	for p, b := range t.byBytes {
		entries = append(entries, TopEntry{Path: p, Reads: t.byReads[p], Bytes: b})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Bytes > entries[j].Bytes
	})
	if n > 0 && n < len(entries) {
		entries = entries[:n]
	}
	return entries
}
