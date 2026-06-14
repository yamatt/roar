package roar

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/nwaples/rardecode/v2"
)

var logger = slog.Default()

const maxReadSize = 1 << 20 // 1MB
const attrValidDuration = 60.0
const entryValidDuration = 60.0

func SetLogger(l *slog.Logger) {
	logger = l
}

type RarFS struct {
	fs.Inode

	sourceDir    string
	inodeCounter uint64
	pathToInode  map[string]uint64
	mu           sync.Mutex
}

type FileEntry struct {
	Name          string
	Size          int64
	ModTime       int64
	IsDir         bool
	ArchivePath   string
	InternalPath  string
	IsPassthrough bool
	SourcePath    string
}

func isRarFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	matched, _ := regexp.MatchString(`\.r\d+$`, lower)
	return matched
}

func isZipFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}

func isFirstRarPart(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".part1.rar") {
		return true
	}
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	return false
}

func archiveBaseName(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".part1.rar") {
		return name[:len(name)-len(".part1.rar")]
	}
	if strings.HasSuffix(lower, ".rar") {
		return name[:len(name)-len(".rar")]
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func normalizeArchivePath(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, "/"), "/")
}

func findRarArchives(dir string) ([]string, error) {
	var archives []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isFirstRarPart(entry.Name()) {
			archives = append(archives, filepath.Join(dir, entry.Name()))
		}
	}
	return archives, nil
}

func findZipArchives(dir string) ([]string, error) {
	var archives []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isZipFile(entry.Name()) {
			archives = append(archives, filepath.Join(dir, entry.Name()))
		}
	}
	return archives, nil
}

func scanArchive(archivePath string) ([]*FileEntry, error) {
	files, err := rardecode.List(archivePath)
	if err != nil {
		return nil, err
	}
	var entries []*FileEntry
	for _, f := range files {
		entries = append(entries, &FileEntry{
			Name:         normalizeArchivePath(f.Name),
			Size:         f.UnPackedSize,
			ModTime:      f.ModificationTime.Unix(),
			IsDir:        f.IsDir,
			ArchivePath:  archivePath,
			InternalPath: normalizeArchivePath(f.Name),
		})
	}
	return entries, nil
}

func scanZipArchive(archivePath string) ([]*FileEntry, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	var entries []*FileEntry
	for _, f := range r.File {
		entries = append(entries, &FileEntry{
			Name:         normalizeArchivePath(f.Name),
			Size:         int64(f.UncompressedSize64),
			ModTime:      f.Modified.Unix(),
			IsDir:        f.FileInfo().IsDir(),
			ArchivePath:  archivePath,
			InternalPath: normalizeArchivePath(f.Name),
		})
	}
	return entries, nil
}

func NewRarFS(sourceDir string) (*RarFS, error) {
	info, err := os.Stat(sourceDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, os.ErrInvalid
	}
	return &RarFS{
		sourceDir:    sourceDir,
		inodeCounter: 1,
		pathToInode:  make(map[string]uint64),
	}, nil
}

type dirEntry struct {
	name  string
	isDir bool
	file  *FileEntry
}

func (r *RarFS) getInode(path string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ino, ok := r.pathToInode[path]; ok {
		return ino
	}
	r.inodeCounter++
	r.pathToInode[path] = r.inodeCounter
	return r.inodeCounter
}

func (r *RarFS) resolveArchivePath(relDir string) (string, string, bool, error) {
	relDir = strings.TrimPrefix(filepath.ToSlash(relDir), ".")
	relDir = strings.TrimPrefix(relDir, "/")
	if relDir == "" {
		return "", "", false, nil
	}

	segments := strings.Split(relDir, "/")
	baseDir := r.sourceDir
	for i, segment := range segments {
		candidateDir := filepath.Join(baseDir, segment)
		if info, err := os.Stat(candidateDir); err == nil && info.IsDir() {
			baseDir = candidateDir
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return "", "", false, err
		}

		archivePath, found, err := findArchiveFile(baseDir, segment)
		if err != nil {
			return "", "", false, err
		}
		if found {
			internalPath := strings.Join(segments[i+1:], "/")
			internalPath = strings.TrimPrefix(strings.TrimPrefix(internalPath, "/"), "/")
			return archivePath, internalPath, true, nil
		}

		return "", "", false, nil
	}

	return "", "", false, nil
}

func findArchiveFile(baseDir, archiveName string) (string, bool, error) {
	candidates := []string{
		filepath.Join(baseDir, archiveName+".zip"),
		filepath.Join(baseDir, archiveName+".rar"),
		filepath.Join(baseDir, archiveName+".part1.rar"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func (r *RarFS) listDirectoryEntries(relDir string) ([]dirEntry, bool, error) {
	relDir = strings.TrimPrefix(filepath.ToSlash(relDir), ".")
	relDir = strings.TrimPrefix(relDir, "/")
	entries := make(map[string]*dirEntry)

	archivePath, internalPrefix, isArchive, err := r.resolveArchivePath(relDir)
	if err != nil {
		return nil, false, err
	}
	if isArchive {
		dirExists, err := r.addArchiveDirectoryEntries(archivePath, internalPrefix, relDir, entries)
		if err != nil {
			return nil, false, err
		}
		if !dirExists {
			return nil, false, nil
		}
		result := make([]dirEntry, 0, len(entries))
		for _, entry := range entries {
			result = append(result, *entry)
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].name < result[j].name
		})
		return result, true, nil
	}

	absDir := filepath.Join(r.sourceDir, relDir)
	dirExists := false

	if info, err := os.Stat(absDir); err == nil && info.IsDir() {
		dirExists = true
		if err := r.addActualSourceDirectoryEntries(absDir, relDir, entries); err != nil {
			return nil, false, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}

	rarFound, err := r.addArchiveVirtualDirectories(absDir, relDir, entries)
	if err != nil {
		return nil, false, err
	}
	if rarFound {
		dirExists = true
	}

	if !dirExists {
		return nil, false, nil
	}

	result := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].name < result[j].name
	})
	return result, true, nil
}

func (r *RarFS) addArchiveDirectoryEntries(archivePath, internalPrefix, relDir string, entries map[string]*dirEntry) (bool, error) {
	var archiveEntries []*FileEntry
	var err error
	if isZipFile(archivePath) {
		archiveEntries, err = scanZipArchive(archivePath)
	} else {
		archiveEntries, err = scanArchive(archivePath)
	}
	if err != nil {
		return false, err
	}

	prefix := strings.TrimPrefix(strings.TrimPrefix(internalPrefix, "/"), "/")
	dirExists := false
	for _, file := range archiveEntries {
		path := file.InternalPath
		if prefix != "" {
			if path == prefix {
				if file.IsDir {
					dirExists = true
				}
				continue
			}
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			path = strings.TrimPrefix(path, prefix+"/")
		}

		parts := strings.Split(path, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		name := parts[0]
		isDir := len(parts) > 1 || file.IsDir
		entry, ok := entries[name]
		if ok {
			entry.isDir = entry.isDir || isDir
			dirExists = true
			continue
		}
		var fileEntry *FileEntry
		if !isDir {
			fileEntry = &FileEntry{
				Name:         filepath.Join(relDir, name),
				Size:         file.Size,
				ModTime:      file.ModTime,
				IsDir:        false,
				ArchivePath:  archivePath,
				InternalPath: file.InternalPath,
			}
		}
		entries[name] = &dirEntry{name: name, isDir: isDir, file: fileEntry}
		dirExists = true
	}
	return dirExists, nil
}

func (r *RarFS) addActualSourceDirectoryEntries(absDir, relDir string, entries map[string]*dirEntry) error {
	items, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	for _, item := range items {
		name := item.Name()
		if item.IsDir() {
			entries[name] = &dirEntry{name: name, isDir: true}
			continue
		}
		if isRarFile(name) && isFirstRarPart(name) {
			archiveName := archiveBaseName(name)
			entries[archiveName] = &dirEntry{name: archiveName, isDir: true}
			continue
		}
		if isZipFile(name) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			logger.Warn("error reading pass-through file info", "file", name, "error", err)
			continue
		}
		entries[name] = &dirEntry{
			name:  name,
			isDir: false,
			file: &FileEntry{
				Name:          filepath.Join(relDir, name),
				Size:          info.Size(),
				ModTime:       info.ModTime().Unix(),
				IsDir:         false,
				IsPassthrough: true,
				SourcePath:    filepath.Join(absDir, name),
			},
		}
	}
	return nil
}

func (r *RarFS) addArchiveVirtualDirectories(absDir, relDir string, entries map[string]*dirEntry) (bool, error) {
	var found bool
	zipArchives, err := findZipArchives(absDir)
	if err != nil {
		return false, err
	}
	for _, archivePath := range zipArchives {
		name := archiveBaseName(filepath.Base(archivePath))
		entries[name] = &dirEntry{name: name, isDir: true}
		found = true
	}
	rarArchives, err := findRarArchives(absDir)
	if err != nil {
		return false, err
	}
	for _, archivePath := range rarArchives {
		name := archiveBaseName(filepath.Base(archivePath))
		entries[name] = &dirEntry{name: name, isDir: true}
		found = true
	}
	return found, nil
}

func (r *RarFS) lookupInDirectory(relDir, name string) (bool, *FileEntry, bool, error) {
	entries, exists, err := r.listDirectoryEntries(relDir)
	if err != nil || !exists {
		return false, nil, false, err
	}
	for _, entry := range entries {
		if entry.name == name {
			return entry.isDir, entry.file, true, nil
		}
	}
	return false, nil, false, nil
}

type RarFSRoot struct {
	fs.Inode
	rfs *RarFS
}

type RarFSDir struct {
	fs.Inode
	rfs *RarFS
	path string
}

type RarFSFile struct {
	fs.Inode
	rfs  *RarFS
	path string
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

func (h *RarFileHandle) ReadAt(dest []byte, off int64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entry.IsPassthrough {
		return readPassthroughFileRange(h.entry.SourcePath, off, int64(len(dest)))
	}
	if isZipFile(h.entry.ArchivePath) {
		return extractZipFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
	}
	return extractFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
}

func readPassthroughFileRange(sourcePath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, err
	}
	result := make([]byte, length)
	n, err := io.ReadFull(file, result)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n == 0 {
			return nil, io.EOF
		}
		return result[:n], nil
	}
	if err != nil {
		return nil, err
	}
	return result[:n], nil
}

func extractZipFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if normalizeArchivePath(f.Name) != internalPath {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, rc, offset); err != nil && err != io.EOF {
				return nil, err
			}
		}
		result := make([]byte, length)
		n, err := io.ReadFull(rc, result)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n == 0 {
				return nil, io.EOF
			}
			return result[:n], nil
		}
		if err != nil {
			return nil, err
		}
		return result[:n], nil
	}
	return nil, os.ErrNotExist
}

func extractFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	reader, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if normalizeArchivePath(header.Name) != internalPath {
			continue
		}
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, reader, offset); err != nil && err != io.EOF {
				return nil, err
			}
		}
		result := make([]byte, length)
		n, err := io.ReadFull(reader, result)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n == 0 {
				return nil, io.EOF
			}
			return result[:n], nil
		}
		if err != nil {
			return nil, err
		}
		return result[:n], nil
	}
	return nil, os.ErrNotExist
}

func Mount(sourceDir, mountPoint string, allowOther bool) (*fuse.Server, *RarFS, error) {
	logger.Info("mounting filesystem", "source", sourceDir, "mountPoint", mountPoint, "allowOther", allowOther)
	rfs, err := NewRarFS(sourceDir)
	if err != nil {
		logger.Error("failed to create filesystem", "error", err)
		return nil, nil, err
	}
	root := &RarFSRoot{rfs: rfs}
	opts := &fs.Options{MountOptions: fuse.MountOptions{AllowOther: allowOther, Debug: false, FsName: "roar", Name: "roar"}}
	server, err := fs.Mount(mountPoint, root, opts)
	if err != nil {
		logger.Error("failed to mount filesystem", "error", err)
		return nil, nil, err
	}
	logger.Info("filesystem mounted successfully")
	return server, rfs, nil
}

func (r *RarFS) Close() error {
	return nil
}
