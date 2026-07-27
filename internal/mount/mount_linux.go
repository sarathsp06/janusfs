//go:build linux

// Package mount is the FUSE adapter, built on github.com/hanwen/go-fuse/v2.
//
// The decision-bearing filesystem (JanusRoot/JanusNode, janus_node.go)
// embeds go-fuse's fs.LoopbackRoot/LoopbackNode and overrides only the
// operations whose behaviour must differ between ALLOWED, MASKED, and HIDDEN.
// Everything else (Lookup, Getattr, Statfs, …) is inherited passthrough
// behaviour.
package mount

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// OpEvent carries the observable fields of a single FUSE operation
// for the adapter's Observe callback.
type OpEvent struct {
	Op        string
	Path      string
	Decision  string
	Bytes     int64
	LatencyUs int64
	Cache     string
}

// Adapter mounts a decision-bearing (Allowed/Masked/Hidden) view of a
// source directory via Linux FUSE.
type Adapter struct {
	// Engine resolves each path's Decision; required.
	Engine *engine.Engine
	// Provider serves redacted bytes for Masked reads; required.
	Provider *provider.RamCache

	// ErrorLogger receives go-fuse diagnostic messages. Wired to fs.Options.Logger.
	ErrorLogger *log.Logger
	// DebugLogger, if non-nil, receives verbose FUSE wire-protocol debug output.
	DebugLogger *log.Logger

	// OnMounted, if set, is called once the mount is established and serving.
	OnMounted func()

	// Observe, if set, is called with an OpEvent for every FUSE operation.
	Observe func(OpEvent)

	server *fuse.Server
}

// Mount blocks until the mount is unmounted.
func (a *Adapter) Mount(ctx context.Context, src, mountpoint string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("mount: source is not a directory: " + src)
	}
	if a.Engine == nil || a.Provider == nil {
		return errors.New("mount: Engine and Provider are required")
	}

	root, jr, err := newJanusRoot(src, a.Engine, a.Provider, a.Observe)
	if err != nil {
		return err
	}
	defer jr.Backing.Close()

	opts := &fs.Options{
		Logger: a.ErrorLogger,
	}
	opts.FsName = "janusfs" // shown as the source in `df -T`
	opts.Name = "janusfs"   // the "fuse.<name>" suffix in `df -T`

	// Zero attribute, entry, and negative-lookup timeouts: a policy reload
	// must take effect on the very next lookup, so the kernel is never allowed
	// to answer from a cached dentry or attribute — otherwise a file freshly
	// tightened from Allowed to Masked/Hidden could keep serving real bytes
	// through a still-cached lookup. This costs an upcall per lookup; that is
	// the intended trade, not an oversight. (Synthetic .janusfs nodes are
	// unaffected — they set their own longer timeouts directly, since their
	// content isn't policy-governed.)
	zeroTimeout := time.Duration(0)
	opts.AttrTimeout = &zeroTimeout
	opts.EntryTimeout = &zeroTimeout
	opts.NegativeTimeout = &zeroTimeout

	if a.DebugLogger != nil {
		opts.Debug = true
		opts.MountOptions.Logger = a.DebugLogger
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return err
	}
	a.server = server
	if a.OnMounted != nil {
		a.OnMounted()
	}

	// Unmount on context cancellation (the SIGINT/SIGTERM path in
	// cmd/janusfs). server.Wait below returns once the unmount completes.
	go func() {
		<-ctx.Done()
		_ = server.Unmount()
	}()

	server.Wait()
	return nil
}

// Unmount requests a clean unmount.
func (a *Adapter) Unmount(mountpoint string) error {
	if a.server == nil {
		return errors.New("mount: no active mount to unmount")
	}
	return a.server.Unmount()
}
