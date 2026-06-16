package roar

import (
	"os"
	"testing"
)

func TestNewRarFSInvalidPath(t *testing.T) {
	f, err := os.CreateTemp("", "notadir")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	defer os.Remove(f.Name())

	if _, err := NewRarFS(f.Name()); err == nil {
		t.Fatalf("expected error for non-directory path, got nil")
	}
}

func TestGetInodeUniqueness(t *testing.T) {
	tmp := t.TempDir()
	rfs, err := NewRarFS(tmp)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	ino1 := rfs.getInode("/a/b")
	ino2 := rfs.getInode("/a/b")
	if ino1 != ino2 {
		t.Fatalf("expected same inode for same path, got %d and %d", ino1, ino2)
	}

	ino3 := rfs.getInode("/a/c")
	if ino3 == ino1 {
		t.Fatalf("expected different inode for different paths, both %d", ino1)
	}
}
