//go:build darwin || linux

package mount

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/sarathsp06/janusfs/internal/engine"
)

// revocableHandle wraps the passthrough handle returned by fs.LoopbackNode.Open
// and re-checks the caller's decision on every read/write. This closes the
// window between a policy reload and the release of an already-open handle:
// without it, a file that was ALLOWED at open time keeps serving raw bytes
// through the existing fd even after a reload turns it MASKED or HIDDEN.
//
// Embedding *fs.LoopbackFile (the concrete type LoopbackNode.Open returns,
// see go-fuse fs/files.go) promotes every LoopbackFile method — Release,
// Flush, Fsync, Getlk/Setlk/Setlkw, Lseek, Setattr, Getattr,
// PassthroughFd — onto this wrapper unchanged, so the fd is not leaked
// and no attribute path diverges from LoopbackFile's semantics. Only Read
// and Write are overridden here; everything else forwards.
//
// The read-time re-check costs one cache-hit engine.Resolve — memoized
// since PRP 03 into ~55 ns on this machine, well inside NFR-3's per-op
// budget, so this hardening is essentially free on the hot path.
type revocableHandle struct {
	*fs.LoopbackFile
	node *JanusNode
}

// stillAllowed returns 0 if this handle's node is currently ALLOWED and
// EACCES if a reload has tightened its decision. A HIDDEN or MASKED
// verdict arriving on a passthrough handle means the reload made the file
// more sensitive since open; the correct response is to fail the read/
// write closed rather than continue passing bytes through.
func (r *revocableHandle) stillAllowed(op string, start time.Time) syscall.Errno {
	d := r.node.resolve().Decision
	if d == engine.Allowed {
		return 0
	}
	r.node.observe(op, d.String(), 0, start)
	return syscall.EACCES
}

var _ = (fs.FileReader)((*revocableHandle)(nil))

func (r *revocableHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	start := time.Now()
	if errno := r.stillAllowed("read", start); errno != 0 {
		return nil, errno
	}
	return r.LoopbackFile.Read(ctx, dest, off)
}

var _ = (fs.FileWriter)((*revocableHandle)(nil))

func (r *revocableHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	start := time.Now()
	if errno := r.stillAllowed("write", start); errno != 0 {
		return 0, errno
	}
	return r.LoopbackFile.Write(ctx, data, off)
}
