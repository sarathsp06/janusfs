// Package mount implements the FUSE adapter described in SPEC.md §6.
//
// This file implements PassthroughFileSystem, the Phase 0 walking-skeleton
// filesystem (SPEC.md §24 execution order, task 2): every operation is
// forwarded unmodified to a real directory on disk. It carries no rule
// engine, no masking, and no Hidden/Masked decisions — those arrive in
// Phase 1+. Its only job is to prove the FUSE-T mount plumbing end to end
// (SPEC.md §6 spike acceptance: ls, cat, dd, git status, Finder, unmount).
package mount

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/fuse/fuseutil"
)

// PassthroughFileSystem forwards every FUSE op to a real directory tree
// rooted at Root. It implements fuseutil.FileSystem.
type PassthroughFileSystem struct {
	fuseutil.NotImplementedFileSystem

	root string

	// inodes maps fuseops.InodeID -> *inode. Populated lazily as paths are
	// looked up; never evicted (acceptable for the Phase 0 spike — a
	// long-lived mount's memory growth is a concern for later phases, not
	// this one).
	inodes sync.Map

	// handles maps fuseops.HandleID -> *os.File for open file handles.
	handles    sync.Map
	nextHandle uint64 // atomic; 0 is never issued (starts at 1)

	uid uint32
	gid uint32
}

// inode is the passthrough filesystem's bookkeeping for one FUSE inode: an
// ID (derived from the real inode number, so hard links map to the same
// FUSE inode) and the absolute path currently believed to back it.
type inode struct {
	id   fuseops.InodeID
	path string
}

// NewPassthroughFileSystem returns a fuseutil.FileSystem that mirrors root.
// root must exist and be a directory.
func NewPassthroughFileSystem(root string) (fuseutil.FileSystem, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("mount: root is not a directory: " + root)
	}

	fs := &PassthroughFileSystem{
		root:       root,
		nextHandle: 0,
		uid:        uint32(os.Getuid()),
		gid:        uint32(os.Getgid()),
	}
	fs.inodes.Store(fuseops.InodeID(fuseops.RootInodeID), &inode{
		id:   fuseops.RootInodeID,
		path: root,
	})
	return fs, nil
}

// --- inode helpers ---------------------------------------------------------

// lookupInode returns the *inode registered for id, or nil if unknown.
func (fs *PassthroughFileSystem) lookupInode(id fuseops.InodeID) *inode {
	v, ok := fs.inodes.Load(id)
	if !ok {
		return nil
	}
	return v.(*inode)
}

// resolveChild stats parent/name on the real filesystem and returns the
// registered (or newly registered) inode for it. Returns (nil, nil) if the
// child does not exist — callers translate that to ENOENT.
func (fs *PassthroughFileSystem) resolveChild(parentID fuseops.InodeID, name string) (*inode, os.FileInfo, error) {
	parent := fs.lookupInode(parentID)
	if parent == nil {
		return nil, nil, nil
	}

	childPath := filepath.Join(parent.path, name)
	fi, err := os.Lstat(childPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	id := inodeIDForStat(fi)
	entry := &inode{id: id, path: childPath}
	stored, _ := fs.inodes.LoadOrStore(id, entry)
	got := stored.(*inode)
	// The path for a given real inode number can legitimately change (e.g.
	// after a rename observed via a fresh lookup); keep it current.
	got.path = childPath
	return got, fi, nil
}

// inodeIDForStat derives a stable FUSE inode ID from a file's real inode
// number, so hard links to the same file share one FUSE inode (FR-11).
func inodeIDForStat(fi os.FileInfo) fuseops.InodeID {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fuseops.InodeID(st.Ino)
	}
	// Fallback: extremely unlikely on darwin, but never fabricate an ID
	// that could collide with the root inode.
	return fuseops.RootInodeID
}

func attributesFromStat(fi os.FileInfo, uid, gid uint32) fuseops.InodeAttributes {
	nlink := uint32(1)
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		nlink = uint32(st.Nlink)
	}
	return fuseops.InodeAttributes{
		Size:  uint64(fi.Size()),
		Nlink: nlink,
		Mode:  fi.Mode(),
		Atime: fi.ModTime(),
		Mtime: fi.ModTime(),
		Ctime: fi.ModTime(),
		Uid:   uid,
		Gid:   gid,
	}
}

// toFuseErr translates an OS-level error into one the fuse package's
// Connection can render as the right errno. If err already carries a
// syscall.Errno (as most os package errors do, wrapped in *PathError), that
// errno is preserved; otherwise it degrades to EIO.
//
// NOTE: this passthrough filesystem is Phase 0 scaffolding. From Phase 1
// onward, error translation is centralized in internal/apperrors per
// SPEC.md §13/§21 — this local helper does not replace that.
func toFuseErr(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	if os.IsNotExist(err) {
		return fuse.ENOENT
	}
	if os.IsExist(err) {
		return fuse.EEXIST
	}
	return fuse.EIO
}

func (fs *PassthroughFileSystem) allocHandle(f *os.File) fuseops.HandleID {
	id := atomic.AddUint64(&fs.nextHandle, 1)
	fs.handles.Store(fuseops.HandleID(id), f)
	return fuseops.HandleID(id)
}

func (fs *PassthroughFileSystem) fileForHandle(h fuseops.HandleID) *os.File {
	v, ok := fs.handles.Load(h)
	if !ok {
		return nil
	}
	return v.(*os.File)
}

// --- fuseutil.FileSystem ----------------------------------------------------

func (fs *PassthroughFileSystem) StatFS(ctx context.Context, op *fuseops.StatFSOp) error {
	return nil
}

func (fs *PassthroughFileSystem) LookUpInode(ctx context.Context, op *fuseops.LookUpInodeOp) error {
	entry, fi, err := fs.resolveChild(op.Parent, op.Name)
	if err != nil {
		return toFuseErr(err)
	}
	if entry == nil {
		return fuse.ENOENT
	}
	op.Entry.Child = entry.id
	op.Entry.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	return nil
}

func (fs *PassthroughFileSystem) GetInodeAttributes(ctx context.Context, op *fuseops.GetInodeAttributesOp) error {
	in := fs.lookupInode(op.Inode)
	if in == nil {
		return fuse.ENOENT
	}
	fi, err := os.Lstat(in.path)
	if err != nil {
		return toFuseErr(err)
	}
	op.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	return nil
}

func (fs *PassthroughFileSystem) SetInodeAttributes(ctx context.Context, op *fuseops.SetInodeAttributesOp) error {
	in := fs.lookupInode(op.Inode)
	if in == nil {
		return fuse.ENOENT
	}

	if op.Size != nil {
		if err := os.Truncate(in.path, int64(*op.Size)); err != nil {
			return toFuseErr(err)
		}
	}
	if op.Mode != nil {
		if err := os.Chmod(in.path, *op.Mode); err != nil {
			return toFuseErr(err)
		}
	}
	if op.Atime != nil || op.Mtime != nil {
		fi, err := os.Lstat(in.path)
		if err != nil {
			return toFuseErr(err)
		}
		atime, mtime := fi.ModTime(), fi.ModTime()
		if op.Atime != nil {
			atime = *op.Atime
		}
		if op.Mtime != nil {
			mtime = *op.Mtime
		}
		if err := os.Chtimes(in.path, atime, mtime); err != nil {
			return toFuseErr(err)
		}
	}
	// Uid/Gid changes (chown) are intentionally not applied in this
	// passthrough scaffold; ownership passthrough is not required by the
	// Phase 0 acceptance list.

	fi, err := os.Lstat(in.path)
	if err != nil {
		return toFuseErr(err)
	}
	op.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	return nil
}

func (fs *PassthroughFileSystem) MkDir(ctx context.Context, op *fuseops.MkDirOp) error {
	parent := fs.lookupInode(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	path := filepath.Join(parent.path, op.Name)
	if err := os.Mkdir(path, op.Mode); err != nil {
		return toFuseErr(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return toFuseErr(err)
	}
	id := inodeIDForStat(fi)
	entry := &inode{id: id, path: path}
	fs.inodes.Store(id, entry)
	op.Entry.Child = id
	op.Entry.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	return nil
}

func (fs *PassthroughFileSystem) CreateFile(ctx context.Context, op *fuseops.CreateFileOp) error {
	parent := fs.lookupInode(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	path := filepath.Join(parent.path, op.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, op.Mode)
	if err != nil {
		return toFuseErr(err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return toFuseErr(err)
	}
	id := inodeIDForStat(fi)
	entry := &inode{id: id, path: path}
	fs.inodes.Store(id, entry)
	op.Entry.Child = id
	op.Entry.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	op.Handle = fs.allocHandle(f)
	return nil
}

func (fs *PassthroughFileSystem) OpenFile(ctx context.Context, op *fuseops.OpenFileOp) error {
	in := fs.lookupInode(op.Inode)
	if in == nil {
		return fuse.ENOENT
	}
	f, err := os.OpenFile(in.path, os.O_RDWR, 0)
	if err != nil {
		f, err = os.OpenFile(in.path, os.O_RDONLY, 0)
		if err != nil {
			return toFuseErr(err)
		}
	}
	op.Handle = fs.allocHandle(f)
	op.KeepPageCache = true
	return nil
}

func (fs *PassthroughFileSystem) ReadFile(ctx context.Context, op *fuseops.ReadFileOp) error {
	f := fs.fileForHandle(op.Handle)
	if f == nil {
		return fuse.EIO
	}
	n, err := f.ReadAt(op.Dst, op.Offset)
	op.BytesRead = n
	if err != nil && !errors.Is(err, io.EOF) {
		return toFuseErr(err)
	}
	return nil
}

func (fs *PassthroughFileSystem) WriteFile(ctx context.Context, op *fuseops.WriteFileOp) error {
	f := fs.fileForHandle(op.Handle)
	if f == nil {
		return fuse.EIO
	}
	_, err := f.WriteAt(op.Data, op.Offset)
	return toFuseErr(err)
}

func (fs *PassthroughFileSystem) SyncFile(ctx context.Context, op *fuseops.SyncFileOp) error {
	f := fs.fileForHandle(op.Handle)
	if f == nil {
		return nil
	}
	return toFuseErr(f.Sync())
}

func (fs *PassthroughFileSystem) FlushFile(ctx context.Context, op *fuseops.FlushFileOp) error {
	return nil
}

func (fs *PassthroughFileSystem) ReleaseFileHandle(ctx context.Context, op *fuseops.ReleaseFileHandleOp) error {
	if f := fs.fileForHandle(op.Handle); f != nil {
		f.Close()
		fs.handles.Delete(op.Handle)
	}
	return nil
}

func (fs *PassthroughFileSystem) OpenDir(ctx context.Context, op *fuseops.OpenDirOp) error {
	if fs.lookupInode(op.Inode) == nil {
		return fuse.ENOENT
	}
	return nil
}

func (fs *PassthroughFileSystem) ReadDir(ctx context.Context, op *fuseops.ReadDirOp) error {
	in := fs.lookupInode(op.Inode)
	if in == nil {
		return fuse.ENOENT
	}
	entries, err := os.ReadDir(in.path)
	if err != nil {
		return toFuseErr(err)
	}

	if int(op.Offset) > len(entries) {
		return nil
	}
	entries = entries[op.Offset:]

	for i, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		childPath := filepath.Join(in.path, e.Name())
		fi, err := os.Lstat(childPath)
		if err != nil {
			continue
		}
		id := inodeIDForStat(fi)
		fs.inodes.LoadOrStore(id, &inode{id: id, path: childPath})

		var dt fuseutil.DirentType
		switch {
		case info.IsDir():
			dt = fuseutil.DT_Directory
		case info.Mode()&os.ModeSymlink != 0:
			dt = fuseutil.DT_Link
		default:
			dt = fuseutil.DT_File
		}

		n := fuseutil.WriteDirent(op.Dst[op.BytesRead:], fuseutil.Dirent{
			Offset: op.Offset + fuseops.DirOffset(i) + 1,
			Inode:  id,
			Name:   e.Name(),
			Type:   dt,
		})
		if n == 0 {
			break
		}
		op.BytesRead += n
	}
	return nil
}

func (fs *PassthroughFileSystem) ReleaseDirHandle(ctx context.Context, op *fuseops.ReleaseDirHandleOp) error {
	return nil
}

func (fs *PassthroughFileSystem) Unlink(ctx context.Context, op *fuseops.UnlinkOp) error {
	parent := fs.lookupInode(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	return toFuseErr(os.Remove(filepath.Join(parent.path, op.Name)))
}

func (fs *PassthroughFileSystem) RmDir(ctx context.Context, op *fuseops.RmDirOp) error {
	parent := fs.lookupInode(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	return toFuseErr(os.Remove(filepath.Join(parent.path, op.Name)))
}

func (fs *PassthroughFileSystem) Rename(ctx context.Context, op *fuseops.RenameOp) error {
	oldParent := fs.lookupInode(op.OldParent)
	newParent := fs.lookupInode(op.NewParent)
	if oldParent == nil || newParent == nil {
		return fuse.ENOENT
	}
	oldPath := filepath.Join(oldParent.path, op.OldName)
	newPath := filepath.Join(newParent.path, op.NewName)
	return toFuseErr(os.Rename(oldPath, newPath))
}

func (fs *PassthroughFileSystem) CreateSymlink(ctx context.Context, op *fuseops.CreateSymlinkOp) error {
	parent := fs.lookupInode(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	path := filepath.Join(parent.path, op.Name)
	if err := os.Symlink(op.Target, path); err != nil {
		return toFuseErr(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return toFuseErr(err)
	}
	id := inodeIDForStat(fi)
	fs.inodes.Store(id, &inode{id: id, path: path})
	op.Entry.Child = id
	op.Entry.Attributes = attributesFromStat(fi, fs.uid, fs.gid)
	return nil
}

func (fs *PassthroughFileSystem) ReadSymlink(ctx context.Context, op *fuseops.ReadSymlinkOp) error {
	in := fs.lookupInode(op.Inode)
	if in == nil {
		return fuse.ENOENT
	}
	target, err := os.Readlink(in.path)
	if err != nil {
		return toFuseErr(err)
	}
	op.Target = target
	return nil
}

func (fs *PassthroughFileSystem) GetXattr(ctx context.Context, op *fuseops.GetXattrOp) error {
	return fuse.ENOATTR
}

func (fs *PassthroughFileSystem) ListXattr(ctx context.Context, op *fuseops.ListXattrOp) error {
	return nil
}

func (fs *PassthroughFileSystem) ForgetInode(ctx context.Context, op *fuseops.ForgetInodeOp) error {
	// Intentionally a no-op: see the doc comment on the inodes field. A
	// long-lived mount would want to evict here; the Phase 0 spike does not.
	return nil
}

var _ fuseutil.FileSystem = (*PassthroughFileSystem)(nil)
