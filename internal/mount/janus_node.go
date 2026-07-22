//go:build darwin || linux

// JanusRoot/JanusNode implement SPEC.md §6/§7's decision-bearing FUSE
// adapter by embedding fs.LoopbackNode and overriding only the ops FR-7's
// matrix says must differ between ALLOWED/MASKED/HIDDEN — every other op
// (Lookup, Getattr, Statfs, …) is inherited from LoopbackNode unchanged,
// which already gives FR-7's "lookup/getattr: real attrs for all three
// states" for free (no override needed: the real file is always stat'd).
package mount

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/sarathsp06/janusfs/internal/apperrors"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/provider"
)

// JanusRoot is the FUSE root, holding the shared decision engine and
// redacted-content cache every JanusNode consults. Embeds fs.LoopbackRoot
// so its Path/Dev fields and default behaviors are inherited; NewNode is
// wired in newJanusRoot to construct JanusNode instead of the default
// LoopbackNode.
type JanusRoot struct {
	fs.LoopbackRoot
	Engine   *engine.Engine
	Provider *provider.RamCache
	Observe  func(OpEvent)
}

// JanusNode is one FUSE node: a passthrough LoopbackNode plus the
// decision-bearing overrides SPEC.md FR-7 requires. isDir is captured at
// construction time (from the real file's stat) rather than re-derived,
// since LoopbackNode's own dir-ness bookkeeping isn't exported.
type JanusNode struct {
	fs.LoopbackNode
	root  *JanusRoot
	isDir bool
}

// newJanusRoot constructs the FUSE root node for src, wired to eng/prov.
// Mirrors fs.NewLoopbackRoot's construction, but with NewNode set to
// produce JanusNode instead of the library's default LoopbackNode (SPEC §6:
// "embedding fs.LoopbackNode and overriding only the decision ops").
func newJanusRoot(src string, eng *engine.Engine, prov *provider.RamCache, observe func(OpEvent)) (fs.InodeEmbedder, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(src, &st); err != nil {
		return nil, err
	}

	jr := &JanusRoot{Engine: eng, Provider: prov, Observe: observe}
	jr.LoopbackRoot.Path = src
	jr.LoopbackRoot.Dev = uint64(st.Dev)
	jr.LoopbackRoot.NewNode = func(rootData *fs.LoopbackRoot, parent *fs.Inode, name string, st *syscall.Stat_t) fs.InodeEmbedder {
		return &JanusNode{
			LoopbackNode: fs.LoopbackNode{RootData: rootData},
			root:         jr,
			isDir:        st.Mode&syscall.S_IFDIR != 0,
		}
	}

	rootNode := jr.LoopbackRoot.NewNode(&jr.LoopbackRoot, nil, "", &st)
	jr.LoopbackRoot.RootNode = rootNode
	return rootNode, nil
}

// isConfigFile reports whether the given relative path is a .janusignore or
// .janusmask file — these must be read-only through the mount (agents must
// never be able to weaken policy by editing config files).
func isConfigFile(relPath string) bool {
	base := path.Base(relPath)
	return base == ".janusignore" || base == ".janusmask"
}

// observe emits a FR-22 event about this operation if an Observe callback
// is configured on the root. Callers that need to report bytes or latency
// construct the OpEvent directly.
func (n *JanusNode) observe(op, decision string, bytes int64, start time.Time) {
	if n.root.Observe == nil {
		return
	}
	var latency int64
	if !start.IsZero() {
		latency = time.Since(start).Microseconds()
	}
	n.root.Observe(OpEvent{
		Op:        op,
		Path:      n.relPath(),
		Decision:  decision,
		Bytes:     bytes,
		LatencyUs: latency,
	})
}

// relPath returns this node's path relative to the mount root, slash
// separated — the form internal/engine.Resolve expects.
func (n *JanusNode) relPath() string {
	return filepath.ToSlash(n.Path(n.Root()))
}

// absPath returns the real, on-disk absolute path this node represents.
func (n *JanusNode) absPath() string {
	return filepath.Join(n.root.LoopbackRoot.Path, n.relPath())
}

// resolve returns this node's own Decision (FR-5..FR-9), recovering from
// any panic in the engine call into a fail-closed Hidden result (NFR-6) —
// the one piece of that invariant this package is responsible for, since
// go-fuse's own dispatch loop does not know about internal/engine.
func (n *JanusNode) resolve() (res engine.Resolution) {
	defer func() {
		if r := recover(); r != nil {
			res = engine.Resolution{Decision: engine.Hidden, Poisoned: true}
		}
	}()
	return n.root.Engine.Resolve(n.relPath(), n.isDir)
}

// decisionFor resolves the Decision for a not-yet-looked-up child name of
// this (directory) node — used by ops invoked with a parent+name rather
// than on the target node itself (Unlink, Rename, Symlink, Link, Mknod,
// Mkdir, Rmdir). isDir is taken from a real lstat when the target already
// exists (Rename/Unlink/Rmdir), defaulting to false for an about-to-be-created
// name (Symlink/Link/Mknod never create directories).
func (n *JanusNode) decisionFor(name string) engine.Decision {
	rel := path.Join(n.relPath(), name)
	isDir := false
	if fi, err := os.Lstat(filepath.Join(n.root.LoopbackRoot.Path, filepath.FromSlash(rel))); err == nil {
		isDir = fi.IsDir()
	}
	return n.root.Engine.Resolve(rel, isDir).Decision
}

var _ = (fs.NodeOpener)((*JanusNode)(nil))

// Open implements FR-7's open matrix: HIDDEN denies unconditionally;
// MASKED denies any write-intent open and otherwise returns a virtual
// handle serving redacted bytes; ALLOWED passes through to LoopbackNode.
// Config files (.janusignore/.janusmask) are unconditionally read-only:
// any write-intent open is denied so an agent cannot weaken policy.
func (n *JanusNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	start := time.Now()
	if isConfigFile(n.relPath()) && flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_CREAT) != 0 {
		n.observe("open", "CONFIG_READONLY", 0, start)
		return nil, 0, syscall.EACCES
	}
	res := n.resolve()
	dec := res.Decision.String()
	defer func() { n.observe("open", dec, 0, start) }()
	switch res.Decision {
	case engine.Hidden:
		return nil, 0, syscall.EACCES
	case engine.Masked:
		if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_CREAT) != 0 {
			return nil, 0, syscall.EACCES
		}
		return &maskedHandle{node: n}, fuse.FOPEN_DIRECT_IO, 0
	default:
		return n.LoopbackNode.Open(ctx, flags)
	}
}

var _ = (fs.NodeOpendirHandler)((*JanusNode)(nil))

// OpendirHandle implements FR-8: a HIDDEN directory denies opendir/readdir
// of itself (its name still appears in its parent's listing — that's the
// parent's Readdir, untouched here — only descending into it is denied).
func (n *JanusNode) OpendirHandle(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("readdir", dec, 0, start) }()
	if d == engine.Hidden {
		return nil, 0, syscall.EACCES
	}
	return n.LoopbackNode.OpendirHandle(ctx, flags)
}

var _ = (fs.NodeSetattrer)((*JanusNode)(nil))

// Setattr implements FR-7's chmod/chown/utimens/truncate row: passthrough
// only when this node itself is ALLOWED.
func (n *JanusNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	start := time.Now()
	if isConfigFile(n.relPath()) {
		n.observe("setattr", "CONFIG_READONLY", 0, start)
		return syscall.EACCES
	}
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("setattr", dec, 0, start) }()
	if d != engine.Allowed {
		return syscall.EACCES
	}
	return n.LoopbackNode.Setattr(ctx, f, in, out)
}

var _ = (fs.NodeUnlinker)((*JanusNode)(nil))

func (n *JanusNode) Unlink(ctx context.Context, name string) syscall.Errno {
	start := time.Now()
	childRel := path.Join(n.relPath(), name)
	if isConfigFile(childRel) {
		n.observe("unlink", "CONFIG_READONLY", 0, start)
		return syscall.EACCES
	}
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("unlink", dec, 0, start) }()
	if d != engine.Allowed {
		return syscall.EACCES
	}
	return n.LoopbackNode.Unlink(ctx, name)
}

var _ = (fs.NodeMkdirer)((*JanusNode)(nil))

func (n *JanusNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("mkdir", dec, 0, start) }()
	if d == engine.Hidden {
		return nil, syscall.EACCES
	}
	return n.LoopbackNode.Mkdir(ctx, name, mode, out)
}

var _ = (fs.NodeRmdirer)((*JanusNode)(nil))

func (n *JanusNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("rmdir", dec, 0, start) }()
	if d == engine.Hidden {
		return syscall.EACCES
	}
	return n.LoopbackNode.Rmdir(ctx, name)
}

var _ = (fs.NodeRenamer)((*JanusNode)(nil))

// Rename implements FR-7's "rename (as source or target)" row: denied if
// either the source or the destination name resolves non-Allowed.
func (n *JanusNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	start := time.Now()

	// Deny renaming a config file or overwriting one.
	srcRel := path.Join(n.relPath(), name)
	if isConfigFile(srcRel) {
		n.observe("rename", "CONFIG_READONLY", 0, start)
		return syscall.EACCES
	}
	if np, ok := newParent.(*JanusNode); ok {
		dstRel := path.Join(np.relPath(), newName)
		if isConfigFile(dstRel) {
			n.observe("rename", "CONFIG_READONLY", 0, start)
			return syscall.EACCES
		}
	}

	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("rename", dec, 0, start) }()
	if d != engine.Allowed {
		return syscall.EACCES
	}
	if np, ok := newParent.(*JanusNode); ok {
		if np.decisionFor(newName) != engine.Allowed {
			return syscall.EACCES
		}
	}
	return n.LoopbackNode.Rename(ctx, name, newParent, newName, flags)
}

var _ = (fs.NodeSymlinker)((*JanusNode)(nil))

func (n *JanusNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("symlink", dec, 0, start) }()
	if d != engine.Allowed {
		return nil, syscall.EACCES
	}
	return n.LoopbackNode.Symlink(ctx, target, name, out)
}

var _ = (fs.NodeLinker)((*JanusNode)(nil))

func (n *JanusNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("link", dec, 0, start) }()
	if d != engine.Allowed {
		return nil, syscall.EACCES
	}
	return n.LoopbackNode.Link(ctx, target, name, out)
}

var _ = (fs.NodeMknoder)((*JanusNode)(nil))

func (n *JanusNode) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("mknod", dec, 0, start) }()
	if d != engine.Allowed {
		return nil, syscall.EACCES
	}
	return n.LoopbackNode.Mknod(ctx, name, mode, rdev, out)
}

var _ = (fs.NodeReadlinker)((*JanusNode)(nil))

// Readlink implements FR-7 (passthrough for Allowed/Masked, EACCES for
// Hidden) plus FR-10: a target that resolves outside the source tree is
// served as dangling (ENOENT) rather than handed to the caller, so the
// mount can never become an escape hatch to an unprotected path.
func (n *JanusNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("readlink", dec, 0, start) }()
	if d == engine.Hidden {
		return nil, syscall.EACCES
	}
	target, errno := n.LoopbackNode.Readlink(ctx)
	if errno != 0 {
		return target, errno
	}
	if escapesRoot(n.root.LoopbackRoot.Path, n.absPath(), string(target)) {
		return nil, syscall.ENOENT
	}
	return target, 0
}

// escapesRoot reports whether target (a symlink's raw content, found at
// symlinkAbsPath) resolves outside rootAbs (FR-10).
func escapesRoot(rootAbs, symlinkAbsPath, target string) bool {
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(symlinkAbsPath), resolved)
	}
	resolved = filepath.Clean(resolved)
	rootAbs = filepath.Clean(rootAbs)
	return resolved != rootAbs && !strings.HasPrefix(resolved, rootAbs+string(filepath.Separator))
}

var _ = (fs.NodeGetxattrer)((*JanusNode)(nil))

func (n *JanusNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("getxattr", dec, 0, start) }()
	if d == engine.Hidden {
		return 0, syscall.EACCES
	}
	return n.LoopbackNode.Getxattr(ctx, attr, dest)
}

var _ = (fs.NodeSetxattrer)((*JanusNode)(nil))

func (n *JanusNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("setxattr", dec, 0, start) }()
	if d != engine.Allowed {
		return syscall.EACCES
	}
	return n.LoopbackNode.Setxattr(ctx, attr, data, flags)
}

var _ = (fs.NodeRemovexattrer)((*JanusNode)(nil))

func (n *JanusNode) Removexattr(ctx context.Context, attr string) syscall.Errno {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("removexattr", dec, 0, start) }()
	if d != engine.Allowed {
		return syscall.EACCES
	}
	return n.LoopbackNode.Removexattr(ctx, attr)
}

// maskedHandle is the "virtual handle" FR-7's open(O_RDONLY) row promises
// for a MASKED file: reads are served by internal/provider, never by
// passing the real fd through. FOPEN_DIRECT_IO (set in JanusNode.Open) asks
// the kernel to skip page-cache and call Read on every access, so a
// content or pattern-set change (FR-20) can never be masked by a stale
// cached page.
type maskedHandle struct {
	node *JanusNode
}

var _ = (fs.FileReader)((*maskedHandle)(nil))

// Read implements FR-7's masked read row and FR-21's backstop: it re-stats
// the real file on every call (never trusts a handle-lifetime-cached key)
// so a concurrent edit is always caught, and recovers any panic from the
// provider/redact path into EIO (NFR-6) rather than crashing the mount.
func (h *maskedHandle) Read(ctx context.Context, dest []byte, off int64) (result fuse.ReadResult, errno syscall.Errno) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			result, errno = nil, apperrors.ToErrno(fmt.Errorf("%w: %v", apperrors.ErrPanic, r))
		}
	}()

	// Re-resolve on every read so policy changes (FR-18 / FR-20) are picked
	// up immediately — the pattern set at open time may be stale after a
	// config-file reload.
	res := h.node.resolve()
	switch res.Decision {
	case engine.Hidden:
		if h.node.root.Observe != nil {
			h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "HIDDEN", LatencyUs: time.Since(start).Microseconds()})
		}
		return nil, syscall.EACCES
	case engine.Masked:
		// Fall through to redacted read with current patterns.
	case engine.Allowed:
		// File no longer masked — fall through to raw read.
		return h.readRaw(ctx, dest, off, start)
	}

	key, err := h.contentKey()
	if err != nil {
		if h.node.root.Observe != nil {
			h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "MASKED", LatencyUs: time.Since(start).Microseconds()})
		}
		return nil, syscall.EIO
	}
	n, err := h.node.root.Provider.ReadAt(ctx, key, res.Patterns, dest, off)
	if err != nil {
		if h.node.root.Observe != nil {
			h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "MASKED", LatencyUs: time.Since(start).Microseconds()})
		}
		return nil, apperrors.ToErrno(err)
	}
	if h.node.root.Observe != nil {
		h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "MASKED", Bytes: int64(n), LatencyUs: time.Since(start).Microseconds(), Cache: "na"})
	}
	return fuse.ReadResultData(dest[:n]), 0
}

// readRaw is a fast-path for files that were masked at open time but are now
// ALLOWED after a policy reload. It reads directly from the underlying real
// file, bypassing the redaction pipeline entirely.
func (h *maskedHandle) readRaw(ctx context.Context, dest []byte, off int64, start time.Time) (result fuse.ReadResult, errno syscall.Errno) {
	f, err := os.Open(h.node.absPath())
	if err != nil {
		if h.node.root.Observe != nil {
			h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "ALLOWED", LatencyUs: time.Since(start).Microseconds()})
		}
		return nil, apperrors.ToErrno(fmt.Errorf("readRaw open %q: %w", h.node.absPath(), err))
	}
	defer f.Close()
	n, err := f.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, apperrors.ToErrno(err)
	}
	if h.node.root.Observe != nil {
		h.node.root.Observe(OpEvent{Op: "read", Path: h.node.relPath(), Decision: "ALLOWED", Bytes: int64(n), LatencyUs: time.Since(start).Microseconds(), Cache: "na"})
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *maskedHandle) contentKey() (provider.ContentKey, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(h.node.absPath(), &st); err != nil {
		return provider.ContentKey{}, err
	}
	return provider.ContentKey{
		Path:    h.node.absPath(),
		MTimeNS: getMtimeNS(&st),
		Size:    st.Size,
		Inode:   st.Ino,
		Gen:     h.node.root.Engine.Generation(),
	}, nil
}
