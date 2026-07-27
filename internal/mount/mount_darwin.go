//go:build darwin

// Package mount is the FUSE adapter, built on github.com/hanwen/go-fuse/v2 over
// macFUSE. It replaces an earlier jacobsa/fuse + FUSE-T stack whose NFS
// transport made mounting unreliable.
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
// source directory via macFUSE.
type Adapter struct {
	// Engine resolves each path's Decision; required.
	Engine *engine.Engine
	// Provider serves redacted bytes for Masked reads; required.
	Provider *provider.RamCache

	// ErrorLogger receives go-fuse diagnostic messages: conditions where the
	// server cannot return an error to the caller but wants to signal that
	// something looks off. Wired to fs.Options.Logger.
	ErrorLogger *log.Logger
	// DebugLogger, if non-nil, receives verbose FUSE wire-protocol debug
	// output and turns on Debug mode. Wired to fuse.MountOptions.Logger.
	DebugLogger *log.Logger

	// OnMounted, if set, is called once the mount is established and serving
	// — after fs.Mount has returned and the kernel handshake has completed —
	// but before Mount blocks waiting for unmount. This lets a caller (e.g.
	// the CLI's success status block) know exactly when it's safe to report the
	// mount as up, without changing Mount's blocking contract.
	OnMounted func()

	// Observe, if set, is called with an OpEvent for every FUSE operation the
	// adapter processes. It must not block — the FUSE op calls it
	// synchronously.
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
		// fs.Options.Logger is the filesystem-level diagnostic sink; it
		// shadows (and is distinct from) MountOptions.Logger below.
		Logger: a.ErrorLogger,
	}
	opts.FsName = "janusfs" // shown as the source in `df -T`
	opts.Name = "janusfs"   // the "fuse.<name>" suffix in `df -T`
	// NullPermissions: let the kernel handle permission checks against the
	// reported mode bits rather than having go-fuse do it — avoids spurious
	// EACCES for root vs. user ownership mismatches on the loopback.
	opts.NullPermissions = true

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

	// macFUSE holds a volume busy by default: Spotlight (mdworker) indexes it
	// and Finder browses it, so a graceful unmount fails with EBUSY forever.
	// nobrowse keeps it out of Finder + Spotlight; noappledouble stops the
	// ._* / .DS_Store writes Finder would otherwise make. Both are the
	// standard cure for "macFUSE won't unmount".
	opts.Options = append(opts.Options, "nobrowse", "noappledouble")
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
