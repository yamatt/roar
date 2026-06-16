package roar

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestRarFSNodeGetattrAndReaddir(t *testing.T) {
	tmp := t.TempDir()
	// create a zip archive to act as a virtual dir
	archivePath := filepath.Join(tmp, "archive.zip")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	if _, err := w.Create("hello.txt"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	w.Close()
	f.Close()

	rfs, err := NewRarFS(tmp)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	root := &RarFSRoot{rfs: rfs}
	var out fuse.AttrOut
	if errno := root.Getattr(context.Background(), nil, &out); errno != 0 {
		t.Fatalf("Getattr root returned error: %v", errno)
	}
	if out.Mode&syscall.S_IFDIR == 0 {
		t.Fatalf("expected root to be directory")
	}

	// Readdir should list entries (archive virtual dir)
	dirs, errno := root.Readdir(context.Background())
	if errno != 0 {
		t.Fatalf("Readdir root errno: %v", errno)
	}
	_ = dirs

	// Create a fake file node and check Getattr
	entry := &FileEntry{Name: "readme.txt", Size: 123}
	fileNode := &RarFSFile{rfs: rfs, path: "readme.txt", entry: entry}
	var fout fuse.AttrOut
	if errno := fileNode.Getattr(context.Background(), nil, &fout); errno != 0 {
		t.Fatalf("file Getattr returned errno: %v", errno)
	}
	if fout.Size != uint64(123) {
		t.Fatalf("expected size 123, got %d", fout.Size)
	}
}
