package roar

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type RarFSRoot struct {
	fs.Inode
	rfs *RarFS
}

type RarFSDir struct {
	fs.Inode
	rfs  *RarFS
	path string
}

type RarFSFile struct {
	fs.Inode
	rfs   *RarFS
	path  string
	entry *FileEntry
}

var _ fs.NodeReaddirer = (*RarFSRoot)(nil)
var _ fs.NodeLookuper = (*RarFSRoot)(nil)
var _ fs.NodeGetattrer = (*RarFSRoot)(nil)
var _ fs.NodeReaddirer = (*RarFSDir)(nil)
var _ fs.NodeLookuper = (*RarFSDir)(nil)
var _ fs.NodeGetattrer = (*RarFSDir)(nil)
var _ fs.NodeGetattrer = (*RarFSFile)(nil)
var _ fs.NodeOpener = (*RarFSFile)(nil)
var _ fs.NodeReader = (*RarFSFile)(nil)

func (r *RarFSRoot) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Ino = 1
	out.SetTimeout(attrValidDuration)
	return 0
}

func (r *RarFSRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, exists, err := r.rfs.listDirectoryEntries("")
	if err != nil || !exists {
		return nil, syscall.ENOENT
	}
	var dirEntries []fuse.DirEntry
	for _, entry := range entries {
		mode := syscall.S_IFREG
		if entry.isDir {
			mode = syscall.S_IFDIR
		}
		ino := r.rfs.getInode(entry.name)
		dirEntries = append(dirEntries, fuse.DirEntry{Name: entry.name, Mode: uint32(mode), Ino: ino})
	}
	return fs.NewListDirStream(dirEntries), 0
}

func (r *RarFSRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	isDir, file, found, err := r.rfs.lookupInDirectory("", name)
	if err != nil {
		return nil, syscall.EIO
	}
	if !found {
		return nil, syscall.ENOENT
	}
	ino := r.rfs.getInode(name)
	if isDir {
		child := &RarFSDir{rfs: r.rfs, path: name}
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
	}
	child := &RarFSFile{rfs: r.rfs, path: name, entry: file}
	out.Size = uint64(file.Size)
	out.SetEntryTimeout(entryValidDuration)
	out.SetAttrTimeout(attrValidDuration)
	return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino}), 0
}

func (d *RarFSDir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Ino = d.rfs.getInode(d.path)
	out.SetTimeout(attrValidDuration)
	return 0
}

func (d *RarFSDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, exists, err := d.rfs.listDirectoryEntries(d.path)
	if err != nil || !exists {
		return nil, syscall.ENOENT
	}
	var dirEntries []fuse.DirEntry
	for _, entry := range entries {
		mode := syscall.S_IFREG
		if entry.isDir {
			mode = syscall.S_IFDIR
		}
		ino := d.rfs.getInode(filepath.Join(d.path, entry.name))
		dirEntries = append(dirEntries, fuse.DirEntry{Name: entry.name, Mode: uint32(mode), Ino: ino})
	}
	return fs.NewListDirStream(dirEntries), 0
}

func (d *RarFSDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	isDir, file, found, err := d.rfs.lookupInDirectory(d.path, name)
	if err != nil {
		return nil, syscall.EIO
	}
	if !found {
		return nil, syscall.ENOENT
	}
	fullPath := filepath.Join(d.path, name)
	ino := d.rfs.getInode(fullPath)
	if isDir {
		child := &RarFSDir{rfs: d.rfs, path: fullPath}
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
	}
	child := &RarFSFile{rfs: d.rfs, path: fullPath, entry: file}
	out.Size = uint64(file.Size)
	out.SetEntryTimeout(entryValidDuration)
	out.SetAttrTimeout(attrValidDuration)
	return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino}), 0
}

func (f *RarFSFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(f.entry.Size)
	out.Ino = f.rfs.getInode(f.path)
	out.SetTimeout(attrValidDuration)
	out.SetTimes(nil, nil, nil)
	return 0
}

func (f *RarFSFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &RarFileHandle{entry: f.entry}, fuse.FOPEN_KEEP_CACHE, 0
}

func (f *RarFSFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	rfh, ok := fh.(*RarFileHandle)
	if !ok {
		return nil, syscall.EIO
	}
	data, err := rfh.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(data), 0
}

type RarFileHandle struct {
	entry *FileEntry
	mu    sync.Mutex
}
