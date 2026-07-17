//go:build darwin

package mount

import (
	"context"
	"log"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseutil"
)

// Adapter mounts a PassthroughFileSystem via FUSE-T. It implements
// platform.Mounter (SPEC.md §6/§5) without importing internal/platform, so
// internal/platform stays the single place that names the interface.
type Adapter struct {
	ErrorLogger *log.Logger
	DebugLogger *log.Logger

	// OnMounted, if set, is called once the mount is established and ready
	// to serve — i.e. once fuse.Mount has returned successfully — but
	// before Mount blocks waiting for unmount. This lets a caller (e.g. the
	// CLI's FR-31 status block) know exactly when it's safe to report the
	// mount as up, without changing platform.Mounter's blocking contract.
	OnMounted func()

	mfs *fuse.MountedFileSystem
}

// Mount blocks until the mount is unmounted, per platform.Mounter.
func (a *Adapter) Mount(ctx context.Context, src, mountpoint string) error {
	fsys, err := NewPassthroughFileSystem(src)
	if err != nil {
		return err
	}

	cfg := &fuse.MountConfig{
		FSName:      "janusfs",
		VolumeName:  "JanusFS",
		FuseImpl:    fuse.FUSEImplFuseT,
		ErrorLogger: a.ErrorLogger,
		DebugLogger: a.DebugLogger,
	}

	mfs, err := fuse.Mount(mountpoint, fuseutil.NewFileSystemServer(fsys), cfg)
	if err != nil {
		return err
	}
	a.mfs = mfs
	if a.OnMounted != nil {
		a.OnMounted()
	}

	joinErr := make(chan error, 1)
	go func() { joinErr <- mfs.Join(context.Background()) }()

	select {
	case err := <-joinErr:
		return err
	case <-ctx.Done():
		if uerr := fuse.Unmount(mountpoint); uerr != nil {
			return uerr
		}
		return <-joinErr
	}
}

// Unmount requests a clean unmount (FR-2/FR-3).
func (a *Adapter) Unmount(mountpoint string) error {
	return fuse.Unmount(mountpoint)
}
