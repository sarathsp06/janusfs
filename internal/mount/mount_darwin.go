//go:build darwin

// Package mount implements the FUSE adapter described in SPEC.md §6/§7, as
// amended by docs/SPEC_AMENDMENTS.md (2026-07-18): the adapter is built on
// github.com/hanwen/go-fuse/v2 over macFUSE, replacing the earlier
// jacobsa/fuse + FUSE-T stack whose NFS transport made mounting unreliable.
//
// The decision-bearing filesystem (JanusRoot/JanusNode, janus_node.go)
// embeds go-fuse's fs.LoopbackRoot/LoopbackNode and overrides only the ops
// FR-7's ALLOWED/MASKED/HIDDEN matrix says must differ — everything else
// (Lookup, Getattr, Statfs, …) is inherited passthrough behavior.
package mount

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// OpEvent carries the observable fields of a single FUSE operation
// for the adapter's Observe callback (FR-22).
type OpEvent struct {
	Op        string
	Path      string
	Decision  string
	Bytes     int64
	LatencyUs int64
	Cache     string
}

// Adapter mounts a decision-bearing (Allowed/Masked/Hidden) view of a
// source directory via macFUSE (SPEC.md §6/§7).
type Adapter struct {
	// Engine resolves each path's Decision (SPEC §7); required.
	Engine *engine.Engine
	// Provider serves redacted bytes for Masked reads (SPEC §8.3); required.
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
	// the CLI's FR-31 status block) know exactly when it's safe to report the
	// mount as up, without changing Mount's blocking contract.
	OnMounted func()

	// Observe, if set, is called with an OpEvent for every FUSE operation the
	// adapter processes (FR-22). It must not block — the FUSE op calls it
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

	root, err := newJanusRoot(src, a.Engine, a.Provider, a.Observe)
	if err != nil {
		return err
	}

	opts := &fs.Options{
		// fs.Options.Logger is the filesystem-level diagnostic sink; it
		// shadows (and is distinct from) MountOptions.Logger below.
		Logger: a.ErrorLogger,
	}
	opts.FsName = "janusfs" // shown as the source in `df -T`
	opts.Name = "janusfs"   // the "fuse.<name>" suffix in `df -T`
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

// Unmount requests a clean unmount (FR-2/FR-3).
func (a *Adapter) Unmount(mountpoint string) error {
	if a.server == nil {
		return errors.New("mount: no active mount to unmount")
	}
	return a.server.Unmount()
}
