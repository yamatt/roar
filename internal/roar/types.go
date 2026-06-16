package roar

import (
	"log/slog"
	"os"
	"sync"

	"github.com/hanwen/go-fuse/v2/fs"
)

const maxReadSize = 1 << 20 // 1MB
const attrValidDuration = 60.0
const entryValidDuration = 60.0

var logger = slog.Default()

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

type dirEntry struct {
	name  string
	isDir bool
	file  *FileEntry
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
