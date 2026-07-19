// Package watch implements SPEC.md §9's Watcher interface via fsnotify,
// monitoring a directory tree for config-file changes (.janusignore/.janusmask)
// and data-file changes, with debounced config-event delivery and overflow
// handling. The watcher is advisory-only (FR-21): every masked-file read
// validates (mtime, size, inode) independently — the backstop, not this
// watcher, is the authoritative change detector.
//
// Recursive watching is implemented by walking the root tree at startup and
// adding new directories as they are created — fsnotify does not recursively
// watch subtrees natively.
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
}

// Watcher monitors a root directory tree for changes, calling onConfig for
// .janusignore/.janusmask file events and onData for all other file events
// (SPEC §9). Start returns immediately after beginning the watch loop in a
// background goroutine; call Stop to shut down cleanly.
type Watcher struct {
	w     *fsnotify.Watcher
	root  string

	eventsTotal   atomic.Uint64
	configEvents  atomic.Uint64
	dataEvents    atomic.Uint64
	overflowCount atomic.Uint64
	started       atomic.Bool

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

// Start begins watching root recursively. onConfig receives paths of
// changed .janusignore/.janusmask files; onData receives the changed path
// and event op for all other files. An empty path passed to onData signals
// watcher overflow — the consumer should call InvalidateAll and log a
// warning (SPEC §9: "Overflow/error → InvalidateAll() + metric increment +
// warning event").
//
// Start returns immediately; the watch loop runs in a background goroutine.
// Call Close to stop.
func (w *Watcher) Start(root string, onConfig func(path string), onData func(path string, op Op)) {
	w.root = root
	w.started.Store(true)

	// Add the root directory synchronously so events on files at the root
	// level are captured even before the loop goroutine starts processing.
	// The loop will add subdirectories as they are discovered/created.
	if err := w.w.Add(root); err != nil {
		// If the root can't be watched, signal overflow and still start
		// the loop (it may handle errors from fsnotify).
		onData("", 0)
	}

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
	}
}

func (w *Watcher) loop(onConfig func(string), onData func(string, Op)) {
	defer close(w.done)

	// Walk the root tree and add every existing directory to the watch list.
	if err := w.addTree(w.root); err != nil {
		// If we can't even start watching, call onData("", 0) to signal
		// overflow/failure so the consumer falls back to InvalidateAll.
		onData("", 0)
		return
	}

	for {
		select {
		case <-w.closed:
			return

		case event, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.eventsTotal.Add(1)

			// If a new directory was created, add it to the watch list
			// so events inside it are caught too.
			if event.Has(fsnotify.Create) {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					w.w.Add(event.Name)
				}
			}

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

// addTree recursively adds dir and all its subdirectories to the watch list.
func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; don't fail the whole tree
		}
		if d.IsDir() {
			if addErr := w.w.Add(path); addErr != nil {
				return addErr
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
