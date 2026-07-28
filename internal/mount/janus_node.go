//go:build darwin || linux

// JanusRoot and JanusNode are the decision-bearing FUSE adapter: they embed
// go-fuse's loopback types and override only the operations whose behaviour must
// differ between ALLOWED, MASKED, and HIDDEN. Every other operation (Lookup,
// Getattr, Statfs, …) is inherited from LoopbackNode unchanged, which is why
// lookup and getattr report real attributes for all three decisions with no code
// of our own: the real file is always stat'd.
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
	"golang.org/x/sys/unix"

	"github.com/sarathsp06/janusfs/internal/apperrors"
	"github.com/sarathsp06/janusfs/internal/backing"
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
	Engine    *engine.Engine
	Provider  *provider.RamCache
	Observe   func(OpEvent)
	StartTime time.Time

	// Reload is the single rule-set reload entry point (see Adapter.Reload);
	// reloadIfStale calls it so an automatic on-open recompile is identical to
	// a manual `janusfs update`. Nil-safe: reloadIfStale falls back to bumping
	// the engine directly, which is still correct (the decision cache is
	// generation-keyed).
	Reload func() error

	// Backing is a descriptor-relative handle to the source root, acquired
	// once at construction. contentKey, readRaw, and decisionFor's Lstat use
	// it instead of resolving a path string against LoopbackRoot.Path on every
	// call, closing the time-of-check-to-time-of-use window a path-based
	// re-resolution leaves open: the decision and the I/O now share the same
	// resolution rather than each re-deriving it. Mutation operations
	// (Unlink/Rename/Symlink/Link/Mkdir/Setattr's chmod, and the embedded
	// LoopbackNode's own read/write handle and directory-stream plumbing) are
	// not yet routed through Backing — see internal/backing's package doc for
	// why that split is deliberate, not an oversight.
	Backing *backing.Root
}

// JanusNode is one FUSE node: a passthrough LoopbackNode plus the
// decision-bearing overrides. isDir is captured at
// construction time (from the real file's stat) rather than re-derived,
// since LoopbackNode's own dir-ness bookkeeping isn't exported.
type JanusNode struct {
	fs.LoopbackNode
	root  *JanusRoot
	isDir bool
}

// newJanusRoot constructs the FUSE root node for src, wired to eng/prov.
// Mirrors fs.NewLoopbackRoot's construction, but with NewNode set to
// produce JanusNode instead of the library's default LoopbackNode.
func newJanusRoot(src string, eng *engine.Engine, prov *provider.RamCache, observe func(OpEvent), reload func() error) (fs.InodeEmbedder, *JanusRoot, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(src, &st); err != nil {
		return nil, nil, err
	}

	// Acquired before anything else touches src: in path-preserving mode this
	// must happen before the FUSE mount is established over the same path, or
	// resolving src by string here would already re-enter the not-yet-existing
	// mount. In the disjoint model (the only one wired up today) there is no
	// such mount, but acquiring it here regardless means this package has
	// exactly one way of reaching the backing tree, not two.
	br, err := backing.Open(src)
	if err != nil {
		return nil, nil, fmt.Errorf("mount: opening backing root %q: %w", src, err)
	}

	jr := &JanusRoot{Engine: eng, Provider: prov, Observe: observe, StartTime: time.Now(), Reload: reload, Backing: br}
	jr.Path = src
	jr.Dev = uint64(st.Dev)
	// NewNode is deprecated in favor of NodeWrapChilder, but migrating would
	// mean deriving isDir from the already-constructed node rather than the
	// stat struct this factory receives directly — a behavior-relevant change
	// to the decision-bearing constructor, deliberately not bundled into a
	// lint pass.
	jr.LoopbackRoot.NewNode = func(rootData *fs.LoopbackRoot, parent *fs.Inode, name string, st *syscall.Stat_t) fs.InodeEmbedder { //nolint:staticcheck
		return &JanusNode{
			LoopbackNode: fs.LoopbackNode{RootData: rootData},
			root:         jr,
			isDir:        st.Mode&syscall.S_IFDIR != 0,
		}
	}

	rootNode := jr.LoopbackRoot.NewNode(&jr.LoopbackRoot, nil, "", &st) //nolint:staticcheck
	jr.RootNode = rootNode
	return rootNode, jr, nil
}

// isConfigFile reports whether the given relative path is a .janusignore or
// .janusmask file — these must be read-only through the mount (agents must
// never be able to weaken policy by editing config files).
func isConfigFile(relPath string) bool {
	base := path.Base(relPath)
	return base == ".janusignore" || base == ".janusmask"
}

// observe emits an event about this operation if an Observe callback
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

// observeRead emits a read event, the one op that also carries a cache result.
// It is the single place the masked/raw read path names OpEvent, so the read
// handlers below never repeat the nil-check, the latency math, or the wire
// struct. cache is "na" on a served read and "" on an early error.
func (n *JanusNode) observeRead(decision string, bytes int64, cache string, start time.Time) {
	if n.root.Observe == nil {
		return
	}
	n.root.Observe(OpEvent{
		Op:        "read",
		Path:      n.relPath(),
		Decision:  decision,
		Bytes:     bytes,
		LatencyUs: time.Since(start).Microseconds(),
		Cache:     cache,
	})
}

// relPath returns this node's path relative to the mount root, slash
// separated — the form internal/engine.Resolve expects.
func (n *JanusNode) relPath() string {
	return filepath.ToSlash(n.Path(n.Root()))
}

// absPath returns the real, on-disk absolute path this node represents.
func (n *JanusNode) absPath() string {
	return filepath.Join(n.root.Path, n.relPath())
}

// resolve returns this node's own Decision, recovering from
// any panic in the engine call into a fail-closed Hidden result —
// the one piece of that invariant this package is responsible for, since
// go-fuse's own dispatch loop does not know about internal/engine.
func (n *JanusNode) resolve() (res engine.Resolution) {
	defer func() {
		if r := recover(); r != nil {
			n.observe("resolve", "PANIC", 0, time.Time{})
			res = engine.Resolution{Decision: engine.Hidden, Poisoned: true}
		}
	}()
	return n.root.Engine.Resolve(n.relPath(), n.isDir)
}

// reloadIfStale checks whether any .janusignore/.janusmask between this
// node and the mount root has appeared, disappeared, or changed on disk
// since the loaded rule-set generation was compiled, and recompiles if so —
// closing the gap where editing (or newly adding) a rule silently has no
// effect until an explicit `janusfs update`. Bounded by path depth (a
// handful of stat calls), so it is only ever called from Open/OpendirHandle,
// never from a read handler's hot path — every read already re-resolves its
// decision against whichever generation is current, so a reload triggered
// here takes effect for already-open handles for free on their next read.
//
// A reload that fails (e.g. a newly-edited file has a syntax error) is
// ignored here: Engine.Reload never installs a broken rule set, so the
// mount keeps serving the last-known-good generation and simply retries
// staleness detection on the next Open/Opendir — the same fail-safe
// contract `janusfs update` already has.
func (n *JanusNode) reloadIfStale() {
	if !n.root.Engine.StaleAncestors(n.relPath(), n.isDir) {
		return
	}
	if n.root.Reload != nil {
		_ = n.root.Reload()
		return
	}
	_ = n.root.Engine.Reload(n.root.Path)
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
	// Descriptor-relative, not a path re-join: the decision this feeds is
	// about to gate a mutation (Unlink/Rename/Symlink/Link/Mkdir/Create), so
	// resolving isDir through the same retained root the eventual I/O will
	// (once mutations are routed through Backing too) go through keeps this
	// check and that I/O looking at the same file, not each re-deriving its
	// own path resolution.
	if st, err := n.root.Backing.LstatAt(rel); err == nil {
		isDir = st.Mode&unix.S_IFMT == unix.S_IFDIR
	}
	return n.root.Engine.Resolve(rel, isDir).Decision
}

// gateClass classifies how a FUSE operation reacts to a path's Decision. It
// collapses the two policy shapes that were otherwise hand-copied across every
// mutating and traversing handler into one named, table-tested mapping — the
// authoritative FR-8 behaviour matrix expressed once.
type gateClass int

const (
	// denyNonAllowed: mutating operations (create, delete, rename, chmod,
	// hardlink, xattr writes) require ALLOWED — both MASKED and HIDDEN deny.
	denyNonAllowed gateClass = iota
	// denyHidden: read/traverse operations (opendir, readlink, xattr reads)
	// and directory create/remove pass ALLOWED and MASKED, denying only
	// HIDDEN. Directories are never MASKED (FR-10), so for mkdir/rmdir the
	// MASKED arm is simply unreachable.
	denyHidden
)

// gate maps a (class, decision) pair to the errno the operation must return,
// or 0 to proceed. This is the single source of truth for how the three faces
// (ALLOWED/MASKED/HIDDEN) translate to access control; every mutating and
// traversing handler consults it instead of re-deriving the check inline.
func gate(class gateClass, d engine.Decision) syscall.Errno {
	switch class {
	case denyNonAllowed:
		if d != engine.Allowed {
			return syscall.EACCES
		}
	case denyHidden:
		if d == engine.Hidden {
			return syscall.EACCES
		}
	}
	return 0
}

var _ = (fs.NodeLookuper)((*JanusNode)(nil))

// Lookup synthesizes the virtual .janusfs directory. Intercepting the name
// before any policy lookup is what makes it impossible for a user rule to hide
// or mask it.
func (n *JanusNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == ".janusfs" && (n.relPath() == "" || n.relPath() == ".") {
		child := &janusVirtualDir{root: n.root}
		stable := fs.StableAttr{
			Mode: syscall.S_IFDIR,
		}
		ino := n.NewInode(ctx, child, stable)
		out.Mode = syscall.S_IFDIR | 0555
		out.SetAttrTimeout(time.Hour)
		out.SetEntryTimeout(time.Hour)
		return ino, 0
	}
	return n.LoopbackNode.Lookup(ctx, name, out)
}

var _ = (fs.NodeReaddirer)((*JanusNode)(nil))

// Readdir injects the synthetic .janusfs entry inside the root directory.
func (n *JanusNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	stream, errno := n.LoopbackNode.Readdir(ctx)
	if errno != 0 {
		return nil, errno
	}

	if n.relPath() == "" || n.relPath() == "." {
		var entries []fuse.DirEntry
		for stream.HasNext() {
			entry, err := stream.Next()
			if err != 0 {
				stream.Close()
				return nil, err
			}
			entries = append(entries, entry)
		}
		stream.Close()

		// Inject .janusfs
		entries = append(entries, fuse.DirEntry{
			Mode: syscall.S_IFDIR | 0555,
			Name: ".janusfs",
		})
		return fs.NewListDirStream(entries), 0
	}

	return stream, errno
}

var _ = (fs.NodeOpener)((*JanusNode)(nil))

// Getattr overrides LoopbackNode to zero the inode number reported to the
// FUSE bridge. LoopbackNode reports the real filesystem inode, but when files
// are replaced (git checkout, editor rename-on-save) the same FUSE node gets a
// new backing inode, causing go-fuse's bridge to log noisy "overriding ino"
// warnings. Zeroing Ino tells go-fuse to use its own synthetic inode numbering.
func (n *JanusNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	errno := n.LoopbackNode.Getattr(ctx, fh, out)
	out.Ino = 0
	return errno
}

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
	n.reloadIfStale()
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
		fh, ff, errno := n.LoopbackNode.Open(ctx, flags)
		if errno != 0 {
			return fh, ff, errno
		}
		// Wrap the passthrough handle so a subsequent policy reload can
		// revoke it: without this, an fd opened while the file was
		// ALLOWED keeps serving raw bytes after the reload turns it
		// MASKED or HIDDEN. If the handle is not a *fs.LoopbackFile
		// (a future go-fuse version might return something else),
		// return it as-is — the correctness gap is what existed before,
		// not a regression.
		if lf, ok := fh.(*fs.LoopbackFile); ok {
			return &revocableHandle{LoopbackFile: lf, node: n}, ff, 0
		}
		return fh, ff, errno
	}
}

var _ = (fs.NodeIoctler)((*JanusNode)(nil))

// Ioctl returns ENOSYS for all ioctl calls. macOS tools (e.g. make) issue
// ioctls on regular files; the default go-fuse LoopbackFile.Ioctl panics on
// empty input buffers (OPCODE-60). Returning ENOSYS tells the kernel this
// filesystem does not support ioctls, which is the correct fail-closed answer.
func (n *JanusNode) Ioctl(ctx context.Context, f fs.FileHandle, cmd uint32, arg uint64, input []byte, output []byte) (int32, syscall.Errno) {
	return 0, syscall.ENOSYS
}

var _ = (fs.NodeOpendirHandler)((*JanusNode)(nil))

// OpendirHandle denies opendir/readdir on a HIDDEN directory
// of itself (its name still appears in its parent's listing — that's the
// parent's Readdir, untouched here — only descending into it is denied).
func (n *JanusNode) OpendirHandle(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	start := time.Now()
	n.reloadIfStale()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("readdir", dec, 0, start) }()
	if e := gate(denyHidden, d); e != 0 {
		return nil, 0, e
	}
	return n.LoopbackNode.OpendirHandle(ctx, flags)
}

var _ = (fs.NodeSetattrer)((*JanusNode)(nil))

// Setattr covers chmod, chown, utimens, and truncate: passthrough only when
// this node itself is ALLOWED.
func (n *JanusNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	start := time.Now()
	if isConfigFile(n.relPath()) {
		n.observe("setattr", "CONFIG_READONLY", 0, start)
		return syscall.EACCES
	}
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("setattr", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return e
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
	if e := gate(denyNonAllowed, d); e != 0 {
		return e
	}
	return n.LoopbackNode.Unlink(ctx, name)
}

var _ = (fs.NodeMkdirer)((*JanusNode)(nil))

func (n *JanusNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("mkdir", dec, 0, start) }()
	if e := gate(denyHidden, d); e != 0 {
		return nil, e
	}
	return n.LoopbackNode.Mkdir(ctx, name, mode, out)
}

var _ = (fs.NodeRmdirer)((*JanusNode)(nil))

func (n *JanusNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("rmdir", dec, 0, start) }()
	if e := gate(denyHidden, d); e != 0 {
		return e
	}
	return n.LoopbackNode.Rmdir(ctx, name)
}

var _ = (fs.NodeRenamer)((*JanusNode)(nil))

// Rename is denied if either the source or the destination name resolves
// non-Allowed, so a masked file cannot be moved to an unmasked name.
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
	if e := gate(denyNonAllowed, d); e != 0 {
		return e
	}
	if np, ok := newParent.(*JanusNode); ok {
		if e := gate(denyNonAllowed, np.decisionFor(newName)); e != 0 {
			return e
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
	if e := gate(denyNonAllowed, d); e != 0 {
		return nil, e
	}
	return n.LoopbackNode.Symlink(ctx, target, name, out)
}

var _ = (fs.NodeLinker)((*JanusNode)(nil))

// Link denies creating a new hardlink unless BOTH the target inode's own path
// and the new name resolve Allowed. Checking only the new name (as an earlier
// version of this method did) lets an agent launder a Masked or Hidden file in
// one syscall: link a masked "secrets.env" to an unmasked "copy.txt", then read
// copy.txt for plaintext. A target that isn't a *JanusNode (not a node this
// mount constructed) is denied outright, since its path can't be established as
// safe.
func (n *JanusNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	tn, ok := target.(*JanusNode)
	if !ok {
		n.observe("link", "HIDDEN", 0, start)
		return nil, syscall.EACCES
	}
	targetDecision := tn.resolve().Decision
	if e := gate(denyNonAllowed, targetDecision); e != 0 {
		n.observe("link", targetDecision.String(), 0, start)
		return nil, e
	}
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("link", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return nil, e
	}
	return n.LoopbackNode.Link(ctx, target, name, out)
}

var _ = (fs.NodeMknoder)((*JanusNode)(nil))

func (n *JanusNode) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	start := time.Now()
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("mknod", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return nil, e
	}
	return n.LoopbackNode.Mknod(ctx, name, mode, rdev, out)
}

var _ = (fs.NodeCreater)((*JanusNode)(nil))

// Create is denied if the target name is a config file or resolves to HIDDEN or
// MASKED.
func (n *JanusNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (inode *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	start := time.Now()
	childRel := path.Join(n.relPath(), name)
	if isConfigFile(childRel) {
		n.observe("create", "CONFIG_READONLY", 0, start)
		return nil, nil, 0, syscall.EACCES
	}
	d := n.decisionFor(name)
	dec := d.String()
	defer func() { n.observe("create", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return nil, nil, 0, e
	}
	return n.LoopbackNode.Create(ctx, name, flags, mode, out)
}

var _ = (fs.NodeReadlinker)((*JanusNode)(nil))

// Readlink passes through for Allowed and Masked and returns EACCES for Hidden.
// A target resolving outside the source tree is served as dangling (ENOENT)
// rather than handed to the caller, so the mount can never become an escape
// hatch to an unprotected path.
func (n *JanusNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("readlink", dec, 0, start) }()
	if e := gate(denyHidden, d); e != 0 {
		return nil, e
	}
	target, errno := n.LoopbackNode.Readlink(ctx)
	if errno != 0 {
		return target, errno
	}
	if escapesRoot(n.root.Path, n.absPath(), string(target)) {
		return nil, syscall.ENOENT
	}
	return target, 0
}

// escapesRoot reports whether target (a symlink's raw content, found at
// symlinkAbsPath) resolves outside rootAbs.
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
	if e := gate(denyHidden, d); e != 0 {
		return 0, e
	}
	return n.LoopbackNode.Getxattr(ctx, attr, dest)
}

var _ = (fs.NodeListxattrer)((*JanusNode)(nil))

// Listxattr is denied (EACCES) if HIDDEN, and passes through otherwise.
func (n *JanusNode) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("listxattr", dec, 0, start) }()
	if e := gate(denyHidden, d); e != 0 {
		return 0, e
	}
	return n.LoopbackNode.Listxattr(ctx, dest)
}

var _ = (fs.NodeSetxattrer)((*JanusNode)(nil))

func (n *JanusNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("setxattr", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return e
	}
	return n.LoopbackNode.Setxattr(ctx, attr, data, flags)
}

var _ = (fs.NodeRemovexattrer)((*JanusNode)(nil))

func (n *JanusNode) Removexattr(ctx context.Context, attr string) syscall.Errno {
	start := time.Now()
	d := n.resolve().Decision
	dec := d.String()
	defer func() { n.observe("removexattr", dec, 0, start) }()
	if e := gate(denyNonAllowed, d); e != 0 {
		return e
	}
	return n.LoopbackNode.Removexattr(ctx, attr)
}

// testRaceHook is a test-only seam (see maskedHandle.Read) for deterministically
// racing a filesystem change into the window between a read's decision and its
// backing I/O. Nil in production; only ever set by a test in this package.
var testRaceHook func()

// maskedHandle is the virtual handle returned for a read-only open of a MASKED
// file: reads are served by internal/provider, never by
// passing the real fd through. FOPEN_DIRECT_IO (set in JanusNode.Open) asks
// the kernel to skip page-cache and call Read on every access, so a
// content or pattern-set change can never be masked by a stale
// cached page.
type maskedHandle struct {
	node *JanusNode
}

var _ = (fs.FileReader)((*maskedHandle)(nil))

// Read serves redacted bytes. It re-stats the real file on every call, never
// trusting a key cached for the handle's lifetime,
// so a concurrent edit is always caught, and recovers any panic from the
// provider/redact path into EIO rather than crashing the mount.
func (h *maskedHandle) Read(ctx context.Context, dest []byte, off int64) (result fuse.ReadResult, errno syscall.Errno) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			h.node.observeRead("PANIC", 0, "", start)
			result, errno = nil, apperrors.ToErrno(fmt.Errorf("%w: %v", apperrors.ErrPanic, r))
		}
	}()

	// Re-resolve on every read so policy changes are picked
	// up immediately — the pattern set at open time may be stale after a
	// config-file reload.
	res := h.node.resolve()
	if testRaceHook != nil {
		// Test-only seam: lets a test deterministically race a filesystem
		// change (e.g. swapping this path for a symlink) into the exact
		// window between the decision above and the backing I/O below,
		// rather than relying on a timing-dependent sleep. Always nil in
		// production.
		testRaceHook()
	}
	switch res.Decision {
	case engine.Hidden:
		h.node.observeRead("HIDDEN", 0, "", start)
		return nil, syscall.EACCES
	case engine.Masked:
		// Fall through to redacted read with current patterns.
	case engine.Allowed:
		// File no longer masked — fall through to raw read.
		return h.readRaw(ctx, dest, off, start)
	}

	key, err := h.contentKey()
	if err != nil {
		h.node.observeRead("MASKED", 0, "", start)
		return nil, syscall.EIO
	}
	n, err := h.node.root.Provider.ReadAt(ctx, key, res.Patterns, dest, off, h.backingOpener())
	if err != nil {
		h.node.observeRead("MASKED", 0, "", start)
		return nil, apperrors.ToErrno(err)
	}
	h.node.observeRead("MASKED", int64(n), "na", start)
	return fuse.ReadResultData(dest[:n]), 0
}

// readRaw is a fast-path for files that were masked at open time but are now
// ALLOWED after a policy reload. It reads directly from the underlying real
// file, bypassing the redaction pipeline entirely.
//
// Opened via the retained backing descriptor rather than a path re-join, with
// O_NOFOLLOW: this decision was made for exactly this path, so if the final
// component is now a symlink, the file the decision was about is not the file
// this call would otherwise open.
func (h *maskedHandle) readRaw(ctx context.Context, dest []byte, off int64, start time.Time) (result fuse.ReadResult, errno syscall.Errno) {
	rel := h.node.relPath()
	fd, err := h.node.root.Backing.OpenAt(rel, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		h.node.observeRead("ALLOWED", 0, "", start)
		return nil, apperrors.ToErrno(fmt.Errorf("readRaw openat %q: %w", rel, err))
	}
	defer func() { _ = unix.Close(fd) }()
	n, err := preadFull(fd, dest, off)
	if err != nil {
		return nil, apperrors.ToErrno(err)
	}
	h.node.observeRead("ALLOWED", int64(n), "na", start)
	return fuse.ReadResultData(dest[:n]), 0
}

// preadFull reads into dest at offset off via pread(2), looping until dest is
// full or EOF, matching os.File.ReadAt's "fill the buffer unless EOF or
// error" contract — a single unix.Pread call may return fewer bytes than
// requested without that being EOF or an error.
func preadFull(fd int, dest []byte, off int64) (int, error) {
	total := 0
	for total < len(dest) {
		n, err := unix.Pread(fd, dest[total:], off+int64(total))
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			break // EOF
		}
	}
	return total, nil
}

func (h *maskedHandle) contentKey() (provider.ContentKey, error) {
	st, err := h.node.root.Backing.StatAt(h.node.relPath())
	if err != nil {
		return provider.ContentKey{}, err
	}
	// absPath() is the cache map's identity key only, never an access path —
	// every actual read above goes through Backing. provider.NewContentKey owns
	// the freshness contract; this call just supplies the stat + generation.
	return provider.NewContentKey(
		h.node.absPath(),
		st.Mtim.Sec*1e9+st.Mtim.Nsec,
		st.Size,
		st.Ino,
		h.node.root.Engine.Generation(),
	), nil
}

// backingOpener returns a provider.Opener that opens this node's real file
// through the retained descriptor rather than by re-resolving its path —
// what the provider's rebuild/oversize path reads is now the same resolution
// contentKey just validated, closing the window a path-string os.Open would
// leave open between the two. O_NOFOLLOW for the same reason as readRaw: this
// decision was made for exactly this path, not for whatever a swapped-in
// symlink might now point to.
func (h *maskedHandle) backingOpener() provider.Opener {
	return func() (io.ReadCloser, error) {
		rel := h.node.relPath()
		fd, err := h.node.root.Backing.OpenAt(rel, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), rel), nil
	}
}
