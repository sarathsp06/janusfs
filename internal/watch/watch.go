// Package watch implements SPEC.md §9's Watcher interface via fsnotify,
// monitoring a directory tree for config-file changes (.janusignore/.janusmask)
// and data-file changes, with debounced config-event delivery and overflow
// handling. The watcher is advisory-only (FR-21): every masked-file read
// validates (mtime, size, inode) independently — the backstop, not this
// watcher, is the authoritative change detector.
//
// Watching is config-focused: at startup a skip-list-aware walk registers the
// root and every directory that already holds a .janusignore/.janusmask, and
// only those. fsnotify does not watch subtrees natively, and on macOS its
// kqueue backend costs a descriptor per watched directory and file, so
// watching a whole large tree exhausts the FD limit; scoping to config-bearing
// directories keeps that bounded. The watcher is advisory (FR-21) — see Start.
//
// Spec intents implemented:
//   - FR-18: detect .janusignore/.janusmask changes, emit onConfig
//   - FR-20: detect data-file changes, emit onData
//   - Watcher overflow/error → onData("", 0) so the consumer can InvalidateAll
package watch

import (
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// maxWatchedDirs caps how many directories the watcher registers with the OS.
// On macOS fsnotify uses kqueue, which holds one open file descriptor per
// watched directory; without a cap, watching a huge tree can exhaust the
// process's — and even the system-wide — file-descriptor limit, starving
// unrelated programs. The watcher is advisory (FR-21): the per-read
// (mtime,size,inode) revalidation is the authoritative change detector, so a
// bounded/partial watch never risks correctness, only hot-reload promptness on
// very large trees.
const maxWatchedDirs = 1024

// DefaultSkipDirs is the built-in set of directory names the watcher skips:
// they hold no JanusFS config, churn heavily, and would burn the descriptor
// budget. Files inside them are still protected on read by the backstop; their
// config (if any) simply won't hot-reload. Override per-watcher via
// Watcher.SkipDirs (config.WatchSkipDirs).
var DefaultSkipDirs = []string{
	"node_modules", ".git", ".hg", ".svn", "vendor", ".venv", "venv",
	"__pycache__", "target", "dist", "build", ".next", ".cache",
	".idea", ".gradle", "Pods", "DerivedData",
}

// shouldSkip reports whether a directory name is in this watcher's skip set
// (Watcher.SkipDirs, or DefaultSkipDirs when unset).
func (w *Watcher) shouldSkip(name string) bool {
	dirs := w.SkipDirs
	if dirs == nil {
		dirs = DefaultSkipDirs
	}
	for _, d := range dirs {
		if d == name {
			return true
		}
	}
	return false
}

// Op describes a filesystem event type (SPEC §9's consumer-facing op).
type Op uint32

const (
	Create Op = 1 << iota
	Write
	Remove
	Rename
	Chmod
)

func (o Op) String() string {
	switch o {
	case Create:
		return "CREATE"
	case Write:
		return "WRITE"
	case Remove:
		return "REMOVE"
	case Rename:
		return "RENAME"
	case Chmod:
		return "CHMOD"
	default:
		return "UNKNOWN"
	}
}

// WatchStats is a point-in-time snapshot of watcher activity (SPEC §9).
type WatchStats struct {
	EventsTotal   uint64
	ConfigEvents  uint64
	DataEvents    uint64
	WatchedDirs   int
	OverflowCount uint64
	Limited       bool // watch coverage was capped (large tree or FD limit)
}

// Watcher monitors a root directory tree for changes, calling onConfig for
// .janusignore/.janusmask file events and onData for all other file events
// (SPEC §9). Start returns immediately after beginning the watch loop in a
// background goroutine; call Stop to shut down cleanly.
type Watcher struct {
	w    *fsnotify.Watcher
	root string

	// SkipDirs overrides DefaultSkipDirs (directory names never watched). Set
	// before Start; nil uses the defaults.
	SkipDirs []string

	eventsTotal   atomic.Uint64
	configEvents  atomic.Uint64
	dataEvents    atomic.Uint64
	overflowCount atomic.Uint64
	started       atomic.Bool
	limited       atomic.Bool // hit the watched-dir cap or an FD limit

	closed   chan struct{}
	done     chan struct{}
}

// New creates a Watcher. No filesystem work happens until Start is called.
func New() (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		w:      w,
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}, nil
}

// Start begins watching root. onConfig receives paths of changed
// .janusignore/.janusmask files; onData receives the changed path and event op
// for other files. An empty path passed to onData signals watcher overflow —
// the consumer should call InvalidateAll and log a warning (SPEC §9).
//
// The watch is config-focused: only the root and directories that already
// contain a .janusignore/.janusmask are registered. On macOS fsnotify/kqueue
// holds a descriptor per watched directory (and file within it), so watching a
// whole large tree exhausts the FD limit; scoping to config-bearing directories
// keeps descriptor use proportional to the number of config files (a handful).
// The watcher is advisory (FR-21) — the per-read (mtime,size,inode) backstop is
// authoritative — so this never affects correctness. Trade-offs: a data-file
// change outside a watched directory isn't pushed as a cache eviction (the
// backstop still revalidates on read), and a brand-new config file created in a
// previously config-less directory is picked up on the next mount.
//
// Start returns immediately; the watch loop runs in a background goroutine.
func (w *Watcher) Start(root string, onConfig func(path string), onData func(path string, op Op)) {
	w.root = root
	w.started.Store(true)

	// Build the watch list synchronously so root-level events are captured
	// immediately.
	w.addConfigDirs(root)

	go w.loop(onConfig, onData)
}

// Stop closes the watcher and waits for the loop goroutine to exit. After
// Stop returns, the watcher is unusable. It is safe to call Stop on a
// watcher that was never started.
func (w *Watcher) Stop() {
	if !w.started.Load() {
		return
	}
	close(w.closed)
	w.w.Close()
	<-w.done
}

// Stats returns a point-in-time snapshot.
func (w *Watcher) Stats() WatchStats {
	return WatchStats{
		EventsTotal:   w.eventsTotal.Load(),
		ConfigEvents:  w.configEvents.Load(),
		DataEvents:    w.dataEvents.Load(),
		WatchedDirs:   len(w.w.WatchList()),
		OverflowCount: w.overflowCount.Load(),
		Limited:       w.limited.Load(),
	}
}

func (w *Watcher) loop(onConfig func(string), onData func(string, Op)) {
	defer close(w.done)

	for {
		select {
		case <-w.closed:
			return

		case event, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.eventsTotal.Add(1)

			if isConfigFile(event.Name) {
				w.configEvents.Add(1)
				onConfig(event.Name)
			} else {
				w.dataEvents.Add(1)
				onData(event.Name, fsnotifyToOp(event))
			}

		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.overflowCount.Add(1)
			// Overflow/error → signal the consumer so it can InvalidateAll.
			onData("", 0)
		}
	}
}

// addConfigDirs watches the root plus every directory that contains a
// .janusignore/.janusmask file, found by a skip-list-aware walk. Descriptor
// use is proportional to the number of config-bearing directories, not the
// tree size. maxWatchedDirs is a safety backstop; an FD error stops expansion
// and marks the watch limited rather than failing the mount (the per-read
// backstop still covers correctness).
func (w *Watcher) addConfigDirs(root string) {
	added := 0
	watched := map[string]bool{}
	add := func(dir string) bool {
		if watched[dir] {
			return true
		}
		if added >= maxWatchedDirs {
			w.limited.Store(true)
			return false
		}
		if err := w.w.Add(dir); err != nil {
			w.limited.Store(true)
			return false
		}
		watched[dir] = true
		added++
		return true
	}

	// Always watch the root so root-level config and activity are seen.
	add(root)

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; don't fail the whole tree
		}
		if d.IsDir() {
			if path != root && w.shouldSkip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isConfigFile(path) {
			if !add(filepath.Dir(path)) {
				return filepath.SkipAll // hit the cap / FD limit
			}
		}
		return nil
	})
}

// isConfigFile returns true if name is a .janusignore or .janusmask file
// (SPEC §9: config events are these two filenames specifically).
func isConfigFile(name string) bool {
	base := filepath.Base(name)
	return base == ".janusignore" || base == ".janusmask"
}

// fsnotifyToOp converts an fsnotify.Event to our Op representation.
func fsnotifyToOp(e fsnotify.Event) Op {
	var op Op
	if e.Has(fsnotify.Create) {
		op |= Create
	}
	if e.Has(fsnotify.Write) {
		op |= Write
	}
	if e.Has(fsnotify.Remove) {
		op |= Remove
	}
	if e.Has(fsnotify.Rename) {
		op |= Rename
	}
	if e.Has(fsnotify.Chmod) {
		op |= Chmod
	}
	return op
}
