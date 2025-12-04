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

// maxReadSize is the maximum size for a single read operation (1MB)
// This prevents excessive memory allocation from large read requests
const maxReadSize = 1 << 20 // 1MB

// attrValidDuration is how long FUSE caches file/directory attributes
// A longer duration reduces Nautilus/file manager refresh issues
const attrValidDuration = 60.0 // 60 seconds

// entryValidDuration is how long FUSE caches directory entry lookups
const entryValidDuration = 60.0 // 60 seconds

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

	// scannedDirs tracks which directories have had their RAR archives scanned
	scannedDirs map[string]bool

	// discoveredDirs tracks which directories have had their subdirectories discovered
	discoveredDirs map[string]bool

	// pendingDirs maps virtual paths to their absolute source paths for lazy scanning
	pendingDirs map[string]string

	// inodeCounter is used to generate stable inode numbers
	inodeCounter uint64

	// pathToInode maps virtual paths to stable inode numbers
	pathToInode map[string]uint64

	mu sync.RWMutex
}

// FileEntry represents a file within a RAR archive or a pass-through file
type FileEntry struct {
	Name         string
	Size         int64
	ModTime      int64
	IsDir        bool
	ArchivePath  string // Path to the .rar file (empty for pass-through files)
	InternalPath string // Path within the archive (empty for pass-through files)
	IsPassthrough bool   // True if this is a pass-through file (not from a RAR archive)
	SourcePath   string // Full path to the source file (for pass-through files)
}

// isRarFile checks if a file is a RAR archive (including split archives)
func isRarFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".rar") {
		return true
	}
	// Match split RAR files like .r00, .r01, .r000, .r001, etc.
	matched, _ := regexp.MatchString(`\.r\d+$`, lower)
	return matched
}

// isFirstRarPart checks if a file is the first part of a RAR archive
// For new-style .partN.rar naming, only .part1.rar is the first part
// For old-style naming, the .rar file (without .partN) is the first part
func isFirstRarPart(name string) bool {
	lower := strings.ToLower(name)

	// Check for new-style .partN.rar naming
	// e.g., split.part1.rar is first, split.part2.rar is not
	if matched, _ := regexp.MatchString(`\.part\d+\.rar$`, lower); matched {
		return strings.HasSuffix(lower, ".part1.rar")
	}

	// For standard .rar files (without .partN), it's the first part
	// The rardecode library handles finding the other parts automatically
	if strings.HasSuffix(lower, ".rar") {
		return true
	}

	// For old-style splits, .r00 might be first, but .rar should exist
	// We'll prefer .rar if it exists
	return false
}

// findRarArchives finds all first-part RAR archives in the given directory
// It identifies the first part of multi-volume archives (e.g., .part1.rar or .rar)
// and returns only those, as the rardecode library handles finding subsequent parts
func findRarArchives(dir string) ([]string, error) {
	var archives []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// Find all first-part RAR files
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

// findPassthroughFiles finds all non-RAR files in the given directory
// These files will be passed through to the virtual filesystem
func findPassthroughFiles(dir string) ([]*FileEntry, error) {
	var files []*FileEntry

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip RAR files (including split archives)
		if isRarFile(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			logger.Warn("error getting file info", "file", entry.Name(), "error", err)
			continue
		}

		file := &FileEntry{
			Name:          entry.Name(),
			Size:          info.Size(),
			ModTime:       info.ModTime().Unix(),
			IsDir:         false,
			IsPassthrough: true,
			SourcePath:    filepath.Join(dir, entry.Name()),
		}
		files = append(files, file)
	}

	return files, nil
}

// scanArchive scans a RAR archive and returns the list of files it contains
// Uses OpenReader to properly handle multi-volume archives
func scanArchive(archivePath string) ([]*FileEntry, error) {
	reader, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()

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
		sourceDir:      sourceDir,
		fileEntries:    make(map[string]*FileEntry),
		directories:    make(map[string][]string),
		scannedDirs:    make(map[string]bool),
		discoveredDirs: make(map[string]bool),
		pendingDirs:    make(map[string]string),
		inodeCounter:   1, // Start at 1; 0 is reserved
		pathToInode:    make(map[string]uint64),
	}

	if err := rfs.scan(); err != nil {
		return nil, err
	}

	return rfs, nil
}

// scan initializes the filesystem by discovering only immediate children of the source directory.
// Subdirectories and their contents are discovered lazily when accessed.
// RAR archive scanning is also deferred until directory contents are accessed.
func (r *RarFS) scan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear existing entries
	r.fileEntries = make(map[string]*FileEntry)
	r.directories = make(map[string][]string)
	r.scannedDirs = make(map[string]bool)
	r.discoveredDirs = make(map[string]bool)
	r.pendingDirs = make(map[string]string)
	r.pathToInode = make(map[string]uint64)
	r.inodeCounter = 1

	logger.Info("initializing filesystem", "path", r.sourceDir)

	// Discover immediate children of the source directory (lazy loading)
	if err := r.discoverDirectoryContentsLocked("", r.sourceDir); err != nil {
		logger.Error("error discovering source directory contents", "error", err)
		return err
	}

	// Mark root as discovered
	r.discoveredDirs[""] = true

	logger.Info("filesystem initialized", "directories", len(r.directories[""]))
	return nil
}

// discoverDirectoryContentsLocked discovers the immediate contents of a directory.
// This includes subdirectories and pass-through files.
// Must be called with the write lock held.
func (r *RarFS) discoverDirectoryContentsLocked(relDir, absPath string) error {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Add subdirectory to the directory listing
			r.addToDirectory(relDir, entry.Name())

			// Store the absolute path for lazy scanning later
			subRelPath := entry.Name()
			if relDir != "" {
				subRelPath = filepath.Join(relDir, entry.Name())
			}
			r.pendingDirs[subRelPath] = filepath.Join(absPath, entry.Name())

			logger.Debug("discovered directory", "path", subRelPath)
		} else if !isRarFile(entry.Name()) {
			// Non-RAR file - add as pass-through
			info, err := entry.Info()
			if err != nil {
				logger.Warn("error getting file info", "file", entry.Name(), "error", err)
				continue
			}

			virtualPath := entry.Name()
			if relDir != "" {
				virtualPath = filepath.Join(relDir, entry.Name())
			}

			file := &FileEntry{
				Name:          virtualPath,
				Size:          info.Size(),
				ModTime:       info.ModTime().Unix(),
				IsDir:         false,
				IsPassthrough: true,
				SourcePath:    filepath.Join(absPath, entry.Name()),
			}

			r.fileEntries[virtualPath] = file
			r.addToDirectory(relDir, entry.Name())

			logger.Debug("discovered pass-through file", "path", virtualPath)
		}
	}

	return nil
}

// ensureDirDiscovered ensures that the subdirectories and pass-through files of a directory have been discovered.
// This is called lazily when directory contents are accessed.
func (r *RarFS) ensureDirDiscovered(relDir string) {
	// Check if already discovered (with read lock first for performance)
	r.mu.RLock()
	if r.discoveredDirs[relDir] {
		r.mu.RUnlock()
		return
	}
	absPath, hasPending := r.pendingDirs[relDir]
	r.mu.RUnlock()

	if !hasPending {
		return
	}

	// Need to discover - acquire write lock
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.discoveredDirs[relDir] {
		return
	}

	logger.Debug("lazy discovering directory contents", "path", relDir)

	// Discover subdirectories and pass-through files
	if err := r.discoverDirectoryContentsLocked(relDir, absPath); err != nil {
		logger.Warn("error discovering directory contents", "path", relDir, "error", err)
	}

	// Mark as discovered
	r.discoveredDirs[relDir] = true
}

// ensureDirScanned ensures that the RAR archives in a directory have been scanned
// This is called lazily when directory contents are accessed
func (r *RarFS) ensureDirScanned(relDir string) {
	// First ensure the directory contents have been discovered
	r.ensureDirDiscovered(relDir)

	// Check if already scanned (with read lock first for performance)
	r.mu.RLock()
	if r.scannedDirs[relDir] {
		r.mu.RUnlock()
		return
	}
	absPath, hasPending := r.pendingDirs[relDir]
	r.mu.RUnlock()

	if !hasPending {
		return
	}

	// Need to scan - acquire write lock
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if r.scannedDirs[relDir] {
		return
	}

	logger.Debug("lazy scanning directory for RAR archives", "path", relDir)

	// Find and process RAR archives
	archives, err := findRarArchives(absPath)
	if err != nil {
		logger.Debug("error finding archives in directory", "path", relDir, "error", err)
	}

	if len(archives) > 0 {
		logger.Info("found RAR archives", "directory", relDir, "count", len(archives))

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
	}

	// Mark as scanned
	r.scannedDirs[relDir] = true
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
// Also assigns a stable inode number to the entry
func (r *RarFS) addToDirectory(dir, name string) {
	fullPath := name
	if dir != "" {
		fullPath = filepath.Join(dir, name)
	}
	for _, existing := range r.directories[dir] {
		if existing == name {
			return
		}
	}
	r.directories[dir] = append(r.directories[dir], name)
	// Assign a stable inode number to this entry
	r.assignInode(fullPath)
}

// getInode returns a stable inode number for a given path
// Must be called with at least a read lock held
func (r *RarFS) getInode(path string) uint64 {
	if ino, ok := r.pathToInode[path]; ok {
		return ino
	}
	// This shouldn't happen often - we assign inodes when creating entries
	return 0
}

// assignInode assigns a stable inode number to a path if not already assigned
// Must be called with a write lock held
func (r *RarFS) assignInode(path string) uint64 {
	if ino, ok := r.pathToInode[path]; ok {
		return ino
	}
	r.inodeCounter++
	r.pathToInode[path] = r.inodeCounter
	return r.inodeCounter
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
var _ fs.NodeGetattrer = (*RarFSRoot)(nil)
var _ fs.NodeReaddirer = (*RarFSDir)(nil)
var _ fs.NodeLookuper = (*RarFSDir)(nil)
var _ fs.NodeGetattrer = (*RarFSDir)(nil)
var _ fs.NodeGetattrer = (*RarFSFile)(nil)
var _ fs.NodeOpener = (*RarFSFile)(nil)
var _ fs.NodeReader = (*RarFSFile)(nil)

// Getattr returns file attributes for root
func (r *RarFSRoot) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	out.Ino = 1 // Root always has inode 1
	out.SetTimeout(attrValidDuration)
	return 0
}

// Readdir lists the contents of the root directory
func (r *RarFSRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	r.rfs.mu.RLock()
	defer r.rfs.mu.RUnlock()

	var entries []fuse.DirEntry

	for _, name := range r.rfs.directories[""] {
		ino := r.rfs.getInode(name)
		entries = append(entries, fuse.DirEntry{
			Name: name,
			Mode: syscall.S_IFDIR,
			Ino:  ino,
		})
	}

	return fs.NewListDirStream(entries), 0
}

// Lookup finds a child node by name
func (r *RarFSRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	r.rfs.mu.RLock()

	// Check if it's a directory
	if _, ok := r.rfs.directories[name]; ok {
		ino := r.rfs.getInode(name)
		r.rfs.mu.RUnlock()
		child := &RarFSDir{rfs: r.rfs, path: name}
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
	}

	// Check if the name exists in root directory listings
	for _, d := range r.rfs.directories[""] {
		if d == name {
			ino := r.rfs.getInode(name)
			r.rfs.mu.RUnlock()
			child := &RarFSDir{rfs: r.rfs, path: name}
			out.SetEntryTimeout(entryValidDuration)
			out.SetAttrTimeout(attrValidDuration)
			return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
		}
	}

	// Check if it's a file in root
	if entry, ok := r.rfs.fileEntries[name]; ok {
		ino := r.rfs.getInode(name)
		r.rfs.mu.RUnlock()
		child := &RarFSFile{rfs: r.rfs, path: name, entry: entry}
		out.Size = uint64(entry.Size)
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return r.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino}), 0
	}

	r.rfs.mu.RUnlock()
	return nil, syscall.ENOENT
}

// Getattr returns file attributes for directory
func (d *RarFSDir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | syscall.S_IFDIR
	d.rfs.mu.RLock()
	out.Ino = d.rfs.getInode(d.path)
	d.rfs.mu.RUnlock()
	out.SetTimeout(attrValidDuration)
	return 0
}

// Readdir lists the contents of a directory
func (d *RarFSDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Ensure RAR archives in this directory have been scanned (lazy loading)
	d.rfs.ensureDirScanned(d.path)

	d.rfs.mu.RLock()
	defer d.rfs.mu.RUnlock()

	var entries []fuse.DirEntry

	for _, name := range d.rfs.directories[d.path] {
		fullPath := filepath.Join(d.path, name)
		ino := d.rfs.getInode(fullPath)
		if _, ok := d.rfs.fileEntries[fullPath]; ok {
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFREG,
				Ino:  ino,
			})
		} else {
			entries = append(entries, fuse.DirEntry{
				Name: name,
				Mode: syscall.S_IFDIR,
				Ino:  ino,
			})
		}
	}

	return fs.NewListDirStream(entries), 0
}

// Lookup finds a child node by name in a directory
func (d *RarFSDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Ensure RAR archives in this directory have been scanned (lazy loading)
	d.rfs.ensureDirScanned(d.path)

	d.rfs.mu.RLock()

	fullPath := filepath.Join(d.path, name)

	// Check if it's a file
	if entry, ok := d.rfs.fileEntries[fullPath]; ok {
		ino := d.rfs.getInode(fullPath)
		d.rfs.mu.RUnlock()
		child := &RarFSFile{rfs: d.rfs, path: fullPath, entry: entry}
		out.Size = uint64(entry.Size)
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG, Ino: ino}), 0
	}

	// Check if it's a directory
	if _, ok := d.rfs.directories[fullPath]; ok {
		ino := d.rfs.getInode(fullPath)
		d.rfs.mu.RUnlock()
		child := &RarFSDir{rfs: d.rfs, path: fullPath}
		out.SetEntryTimeout(entryValidDuration)
		out.SetAttrTimeout(attrValidDuration)
		return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
	}

	// Check if it's in the directory listing
	for _, n := range d.rfs.directories[d.path] {
		if n == name {
			// It's a directory that was implicitly created
			ino := d.rfs.getInode(fullPath)
			d.rfs.mu.RUnlock()
			child := &RarFSDir{rfs: d.rfs, path: fullPath}
			out.SetEntryTimeout(entryValidDuration)
			out.SetAttrTimeout(attrValidDuration)
			return d.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFDIR, Ino: ino}), 0
		}
	}

	d.rfs.mu.RUnlock()
	return nil, syscall.ENOENT
}

// Getattr returns file attributes
func (f *RarFSFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644 | syscall.S_IFREG
	out.Size = uint64(f.entry.Size)
	f.rfs.mu.RLock()
	out.Ino = f.rfs.getInode(f.path)
	f.rfs.mu.RUnlock()
	out.SetTimeout(attrValidDuration)
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
// For pass-through files, reads directly from the source file
func (h *RarFileHandle) ReadAt(dest []byte, off int64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Handle pass-through files
	if h.entry.IsPassthrough {
		logger.Debug("reading pass-through file", "file", h.entry.SourcePath, "offset", off, "size", len(dest))
		return readPassthroughFileRange(h.entry.SourcePath, off, int64(len(dest)))
	}

	logger.Debug("reading file", "file", h.entry.InternalPath, "offset", off, "size", len(dest))

	data, err := extractFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
	if err != nil {
		logger.Warn("error reading file", "file", h.entry.InternalPath, "error", err)
		return nil, err
	}

	return data, nil
}

// readPassthroughFileRange reads a range of bytes from a regular file
func readPassthroughFileRange(sourcePath string, offset, length int64) ([]byte, error) {
	// Cap the read size to prevent excessive memory allocation
	if length > maxReadSize {
		length = maxReadSize
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	// Seek to offset
	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, err
	}

	// Read the requested length
	result := make([]byte, length)
	n, err := io.ReadFull(file, result)
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

// extractFileRange extracts a specific range of bytes from a file in a RAR archive
// This streams through the archive and only reads the requested portion,
// avoiding loading the entire file into memory
// Uses OpenReader to properly handle multi-volume archives
func extractFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	// Cap the read size to prevent excessive memory allocation
	if length > maxReadSize {
		length = maxReadSize
	}

	reader, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()

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
