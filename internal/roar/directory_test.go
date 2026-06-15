package roar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddActualSourceDirectoryEntries_and_Virtuals(t *testing.T) {
	tmp := t.TempDir()
	// create a regular file
	if err := os.WriteFile(filepath.Join(tmp, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	// create a rar first part and a zip file
	if err := os.WriteFile(filepath.Join(tmp, "archive.part1.rar"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "other.zip"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tmp)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	entries := make(map[string]*dirEntry)
	if err := rfs.addActualSourceDirectoryEntries(tmp, "", entries); err != nil {
		t.Fatalf("addActualSourceDirectoryEntries failed: %v", err)
	}

	if _, ok := entries["file.txt"]; !ok {
		t.Fatalf("expected file.txt in entries")
	}
	if e, ok := entries["archive"]; !ok || !e.isDir {
		t.Fatalf("expected archive virtual dir for archive.part1.rar")
	}

	// virtual directories should include both zip and rar
	entries2 := make(map[string]*dirEntry)
	found, err := rfs.addArchiveVirtualDirectories(tmp, "", entries2)
	if err != nil {
		t.Fatalf("addArchiveVirtualDirectories failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found==true when archives exist")
	}
	if _, ok := entries2["archive"]; !ok {
		t.Fatalf("expected archive virtual dir in entries2")
	}
	if _, ok := entries2["other"]; !ok {
		t.Fatalf("expected other virtual dir for other.zip")
	}
}
