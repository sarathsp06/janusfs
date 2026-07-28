//go:build darwin || linux

package mount

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/sarathsp06/janusfs/internal/vfsmeta"
)

// janusVirtualDir represents the synthetic read-only ".janusfs" directory.
type janusVirtualDir struct {
	fs.Inode
	root *JanusRoot
}

var _ = (fs.NodeGetattrer)((*janusVirtualDir)(nil))
var _ = (fs.NodeReaddirer)((*janusVirtualDir)(nil))
var _ = (fs.NodeLookuper)((*janusVirtualDir)(nil))

func (d *janusVirtualDir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0555
	return 0
}

func (d *janusVirtualDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Mode: syscall.S_IFREG | 0444, Name: "conflicts.json"},
		{Mode: syscall.S_IFREG | 0444, Name: "status.json"},
	}
	return fs.NewListDirStream(entries), 0
}

func (d *janusVirtualDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name != "conflicts.json" && name != "status.json" {
		return nil, syscall.ENOENT
	}

	child := &janusVirtualFile{
		root: d.root,
		name: name,
	}

	stable := fs.StableAttr{
		Mode: syscall.S_IFREG,
	}

	ino := d.NewInode(ctx, child, stable)

	content := child.content()
	out.Mode = syscall.S_IFREG | 0444
	out.Size = uint64(len(content))
	out.SetAttrTimeout(time.Hour)
	out.SetEntryTimeout(time.Hour)

	return ino, 0
}

// janusVirtualFile represents the conflicts.json or status.json synthetic file.
type janusVirtualFile struct {
	fs.Inode
	root *JanusRoot
	name string
}

var _ = (fs.NodeGetattrer)((*janusVirtualFile)(nil))
var _ = (fs.NodeOpener)((*janusVirtualFile)(nil))

func (f *janusVirtualFile) content() []byte {
	if f.name == "conflicts.json" {
		b, err := vfsmeta.ConflictsJSON(f.root.LoopbackRoot.Path)
		if err != nil {
			return []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))
		}
		return b
	} else if f.name == "status.json" {
		ps := f.root.Provider.Stats()
		return vfsmeta.StatusJSON(
			f.root.StartTime,
			f.root.Engine.Generation(),
			ps.Entries,
			ps.Bytes,
			ps.Hits,
			ps.Misses,
			ps.Rebuilds,
		)
	}
	return nil
}

func (f *janusVirtualFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	content := f.content()
	out.Mode = syscall.S_IFREG | 0444
	out.Size = uint64(len(content))
	return 0
}

func (f *janusVirtualFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_TRUNC|syscall.O_CREAT) != 0 {
		return nil, 0, syscall.EACCES
	}

	content := f.content()
	return &virtualFileHandle{content: content}, fuse.FOPEN_DIRECT_IO, 0
}

type virtualFileHandle struct {
	content []byte
}

var _ = (fs.FileReader)((*virtualFileHandle)(nil))

func (h *virtualFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(h.content)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(h.content)) {
		end = int64(len(h.content))
	}
	return fuse.ReadResultData(h.content[off:end]), 0
}
