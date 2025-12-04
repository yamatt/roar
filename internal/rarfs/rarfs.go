// Package rarfs provides a FUSE filesystem that presents the contents of RAR archives
// in a directory as if they were regular files.
package rarfs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/nwaples/rardecode/v2"
)

// logger is the package-level logger for rarfs operations
var logger = slog.Default()

// SetLogger sets the logger for the rarfs package
func SetLogger(l *slog.Logger) {
	logger = l
}

// RarFS represents a FUSE filesystem for RAR archives
type RarFS struct {
	fs.Inode

	// sourceDir is the directory containing directories with RAR archives
	sourceDir string

	// fileEntries maps virtual paths to their archive info
	fileEntries map[string]*FileEntry

	// directories maps virtual paths to directory info
	directories map[string][]string

	mu sync.RWMutex
}

// FileEntry represents a file within a RAR archive
type FileEntry struct {
	Name         string
	Size         int64
	ModTime      int64
	IsDir        bool
	ArchivePath  string // Path to the .rar file
	InternalPath string // Path within the archive
}

// isRarFile checks if a file is a RAR archive (including split archives)
func isRarFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	// Match split RAR files like .r00, .r01, etc.
	matched, _ := regexp.MatchString(`\.r\d+$`, lower)
	return matched
}

// isFirstRarPart checks if a file is the first part of a RAR archive
func isFirstRarPart(name string) bool {
	lower := strings.ToLower(name)
	// For split archives, we only want to process the main .rar file or .r00
	// The rardecode library handles finding the other parts automatically
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	// For old-style splits, .r00 might be first, but .rar should exist
	// We'll prefer .rar if it exists
	return false
}

// findRarArchives finds all RAR archives in the given directory
func findRarArchives(dir string) ([]string, error) {
	var archives []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// First pass: find all .rar files
	rarFiles := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".rar") {
			archives = append(archives, filepath.Join(dir, entry.Name()))
			rarFiles[entry.Name()] = true
		}
	}

	// If no .rar files found, look for .r00 or .r01 as starting point
	if len(archives) == 0 {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			lower := strings.ToLower(entry.Name())
			if strings.HasSuffix(lower, ".r00") || strings.HasSuffix(lower, ".r01") {
				// Check if there's a corresponding .rar file
				base := entry.Name()[:len(entry.Name())-4]
				if !rarFiles[base+".rar"] && !rarFiles[base+".RAR"] {
					archives = append(archives, filepath.Join(dir, entry.Name()))
				}
			}
		}
	}

	return archives, nil
}

// scanArchive scans a RAR archive and returns the list of files it contains
func scanArchive(archivePath string) ([]*FileEntry, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	reader, err := rardecode.NewReader(file)
	if err != nil {
		return nil, err
	}

	var entries []*FileEntry

	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		entry := &FileEntry{
			Name:         header.Name,
			Size:         header.UnPackedSize,
			ModTime:      header.ModificationTime.Unix(),
			IsDir:        header.IsDir,
			ArchivePath:  archivePath,
			InternalPath: header.Name,
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// NewRarFS creates a new RarFS rooted at the given source directory
func NewRarFS(sourceDir string) (*RarFS, error) {
	rfs := &RarFS{
		sourceDir:   sourceDir,
		fileEntries: make(map[string]*FileEntry),
		directories: make(map[string][]string),
	}

	if err := rfs.scan(); err != nil {
		return nil, err
	}

	return rfs, nil
}

// scan walks the source directory and builds the virtual filesystem structure
func (r *RarFS) scan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear existing entries
	r.fileEntries = make(map[string]*FileEntry)
	r.directories = make(map[string][]string)

	logger.Info("scanning source directory", "path", r.sourceDir)

	// Walk the source directory recursively
	err := filepath.WalkDir(r.sourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			logger.Warn("error accessing path", "path", path, "error", err)
			return nil // Continue walking despite errors
		}

		// Skip the source directory itself
		if path == r.sourceDir {
			return nil
		}

		// We're only interested in directories that may contain RAR files
		if !d.IsDir() {
			return nil
		}

		// Get the relative path from source directory
		relDir, err := filepath.Rel(r.sourceDir, path)
		if err != nil {
			logger.Warn("error getting relative path", "path", path, "error", err)
			return nil
		}

		logger.Debug("scanning directory", "path", relDir)

		archives, err := findRarArchives(path)
		if err != nil {
			logger.Debug("error finding archives in directory", "path", relDir, "error", err)
			return nil
		}

		if len(archives) == 0 {
			// No archives in this directory, but ensure the directory exists in our structure
			r.ensureDirectoryPath(relDir)
			return nil
		}

		logger.Info("found RAR archives", "directory", relDir, "count", len(archives))

		// Ensure the directory path exists in our structure
		r.ensureDirectoryPath(relDir)

		for _, archivePath := range archives {
			logger.Debug("scanning archive", "archive", archivePath)
			files, err := scanArchive(archivePath)
			if err != nil {
				logger.Warn("error scanning archive", "archive", archivePath, "error", err)
				continue
			}

			logger.Debug("found files in archive", "archive", archivePath, "count", len(files))

			for _, f := range files {
				// Virtual path: relDir/internal_path
				virtualPath := filepath.Join(relDir, f.InternalPath)
				f.Name = virtualPath

				if f.IsDir {
					// Add to directories map
					parentDir := filepath.Dir(virtualPath)
					baseName := filepath.Base(virtualPath)
					r.addToDirectory(parentDir, baseName)
				} else {
					r.fileEntries[virtualPath] = f
					// Ensure parent directories exist in the map
					r.ensureDirectoryPath(filepath.Dir(virtualPath))
					// Add file to its parent directory
					parentDir := filepath.Dir(virtualPath)
					baseName := filepath.Base(virtualPath)
					r.addToDirectory(parentDir, baseName)
				}
			}
		}

		return nil
	})

	if err != nil {
		logger.Error("error walking source directory", "error", err)
		return err
	}

	logger.Info("scan complete", "files", len(r.fileEntries), "directories", len(r.directories))
	return nil
}

// ensureDirectoryPath ensures all parent directories in a path exist in the directories map
func (r *RarFS) ensureDirectoryPath(path string) {
	if path == "" || path == "." {
		return
	}

	parts := strings.Split(filepath.ToSlash(path), "/")
	currentPath := ""

	for i, part := range parts {
		if i == 0 {
			r.addToDirectory("", part)
			currentPath = part
		} else {
			r.addToDirectory(currentPath, part)
			currentPath = filepath.Join(currentPath, part)
		}
	}
}

// addToDirectory adds a name to a directory's listing if not already present
func (r *RarFS) addToDirectory(dir, name string) {
	for _, existing := range r.directories[dir] {
		if existing == name {
			return
		}
	}
	r.directories[dir] = append(r.directories[dir], name)
}

// RarFSRoot is the root node of the FUSE filesystem
type RarFSRoot struct {
	fs.Inode
	rfs *RarFS
}

// RarFSDir is a directory node
type RarFSDir struct {
	fs.Inode
	rfs  *RarFS
	path string
}

// RarFSFile is a file node
type RarFSFile struct {
	fs.Inode
	rfs   *RarFS
	path  string
	entry *FileEntry
}

// Ensure interfaces are implemented
var _ fs.NodeReaddirer = (*RarFSRoot)(nil)
var _ fs.NodeLookuper = (*RarFSRoot)(nil)
var _ fs.NodeReaddirer = (*RarFSDir)(nil)
var _ fs.NodeLookuper = (*RarFSDir)(nil)
var _ fs.NodeGetattrer = (*RarFSFile)(nil)
var _ fs.NodeOpener = (*RarFSFile)(nil)
var _ fs.NodeReader = (*RarFSFile)(nil)

// Getattr returns file attributes for root
func (r *RarFSRoot) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	return 0
}

// Readdir lists the contents of the root directory
func (r *RarFSRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	r.rfs.mu.RLock()
	defer r.rfs.mu.RUnlock()

	var entries []fuse.DirEntry

	for _, name := range r.rfs.directories[""] {
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: syscall.S_IFDIR,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// Lookup finds a child node by name
func (r *RarFSRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	r.rfs.mu.RLock()
	defer r.rfs.mu.RUnlock()

	// Check if it's a directory
	if _, ok := r.rfs.directories[name]; ok {
		child := &RarFSDir{rfs: r.rfs, path: name}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	// Check if the name exists in root directory listings
	for _, d := range r.rfs.directories[""] {
		if d == name {
			child := &RarFSDir{rfs: r.rfs, path: name}
			return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	// Check if it's a file in root
	if entry, ok := r.rfs.fileEntries[name]; ok {
		child := &RarFSFile{rfs: r.rfs, path: name, entry: entry}
		out.Size = uint64(entry.Size)
		return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	return nil, syscall.ENOENT
}

// Getattr returns file attributes for directory
func (d *RarFSDir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	return 0
}

// Readdir lists the contents of a directory
func (d *RarFSDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	d.rfs.mu.RLock()
	defer d.rfs.mu.RUnlock()

	var entries []fuse.DirEntry

	for _, name := range d.rfs.directories[d.path] {
		fullPath := filepath.Join(d.path, name)
		if _, ok := d.rfs.fileEntries[fullPath]; ok {
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFREG,
			})
		} else {
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFDIR,
			})
		}
	}

	return fs.NewListDirStream(entries), 0
}

// Lookup finds a child node by name in a directory
func (d *RarFSDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	d.rfs.mu.RLock()
	defer d.rfs.mu.RUnlock()

	fullPath := filepath.Join(d.path, name)

	// Check if it's a file
	if entry, ok := d.rfs.fileEntries[fullPath]; ok {
		child := &RarFSFile{rfs: d.rfs, path: fullPath, entry: entry}
		out.Size = uint64(entry.Size)
		return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG}), 0
	}

	// Check if it's a directory
	if _, ok := d.rfs.directories[fullPath]; ok {
		child := &RarFSDir{rfs: d.rfs, path: fullPath}
		return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
	}

	// Check if it's in the directory listing
	for _, n := range d.rfs.directories[d.path] {
		if n == name {
			// It's a directory that was implicitly created
			child := &RarFSDir{rfs: d.rfs, path: fullPath}
			return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR}), 0
		}
	}

	return nil, syscall.ENOENT
}

// Getattr returns file attributes
func (f *RarFSFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(f.entry.Size)
	out.SetTimes(nil, nil, nil)
	return 0
}

// Open opens the file for reading
func (f *RarFSFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &RarFileHandle{entry: f.entry}, fuse.FOPEN_KEEP_CACHE, 0
}

// Read reads data from the file
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

// RarFileHandle handles file operations using streaming reads
// This implementation reads data directly from the archive on each read operation,
// avoiding loading entire files into memory. This is suitable for large files.
type RarFileHandle struct {
	entry *FileEntry
	mu    sync.Mutex
}

// ReadAt reads data at the specified offset using streaming extraction
func (h *RarFileHandle) ReadAt(dest []byte, off int64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	logger.Debug("reading file", "file", h.entry.InternalPath, "offset", off, "size", len(dest))

	data, err := extractFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
	if err != nil {
		logger.Warn("error reading file", "file", h.entry.InternalPath, "error", err)
		return nil, err
	}

	return data, nil
}

// extractFileRange extracts a specific range of bytes from a file in a RAR archive
// This streams through the archive and only reads the requested portion,
// avoiding loading the entire file into memory
func extractFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	reader, err := rardecode.NewReader(file)
	if err != nil {
		return nil, err
	}

	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if header.Name == internalPath {
			// Skip to the offset position
			if offset > 0 {
				skipped, err := io.CopyN(io.Discard, reader, offset)
				if err != nil && err != io.EOF {
					return nil, err
				}
				if skipped < offset {
					// Offset is beyond end of file
					return nil, io.EOF
				}
			}

			// Read the requested length
			result := make([]byte, length)
			n, err := io.ReadFull(reader, result)
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// Partial read at end of file
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
	}

	return nil, os.ErrNotExist
}

// Mount mounts the RAR filesystem at the specified mount point
func Mount(sourceDir, mountPoint string) (*fuse.Server, error) {
	logger.Info("mounting filesystem", "source", sourceDir, "mountPoint", mountPoint)

	rfs, err := NewRarFS(sourceDir)
	if err != nil {
		logger.Error("failed to create filesystem", "error", err)
		return nil, err
	}

	root := &RarFSRoot{rfs: rfs}

	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			Debug:      false,
			FsName:     "roar",
			Name:       "roar",
		},
	}

	server, err := fs.Mount(mountPoint, root, opts)
	if err != nil {
		logger.Error("failed to mount filesystem", "error", err)
		return nil, err
	}

	logger.Info("filesystem mounted successfully")
	return server, nil
}
