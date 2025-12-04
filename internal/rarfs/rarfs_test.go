package rarfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsRarFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{"standard rar", "archive.rar", true},
		{"uppercase rar", "ARCHIVE.RAR", true},
		{"mixed case rar", "Archive.Rar", true},
		{"split r00", "archive.r00", true},
		{"split r01", "archive.r01", true},
		{"split r99", "archive.r99", true},
		{"not rar - zip", "archive.zip", false},
		{"not rar - tar", "archive.tar", false},
		{"not rar - partial match", "rarchive.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRarFile(tt.filename); got != tt.want {
				t.Errorf("isRarFile(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsFirstRarPart(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{"standard rar", "archive.rar", true},
		{"uppercase rar", "ARCHIVE.RAR", true},
		{"split r00", "archive.r00", false},
		{"split r01", "archive.r01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFirstRarPart(tt.filename); got != tt.want {
				t.Errorf("isFirstRarPart(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestFindRarArchives(t *testing.T) {
	// Create a temporary directory structure
	tempDir := t.TempDir()

	// Create test files
	rarFile := filepath.Join(tempDir, "test.rar")
	if err := os.WriteFile(rarFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a split rar file
	splitFile := filepath.Join(tempDir, "split.r01")
	if err := os.WriteFile(splitFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-rar file
	txtFile := filepath.Join(tempDir, "readme.txt")
	if err := os.WriteFile(txtFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	archives, err := findRarArchives(tempDir)
	if err != nil {
		t.Fatalf("findRarArchives failed: %v", err)
	}

	// Should find the .rar file
	found := false
	for _, a := range archives {
		if filepath.Base(a) == "test.rar" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected to find test.rar in archives")
	}
}

func TestFindRarArchivesNonExistent(t *testing.T) {
	_, err := findRarArchives("/nonexistent/path")
	if err == nil {
		t.Error("Expected error for non-existent directory")
	}
}

func TestNewRarFSEmptyDir(t *testing.T) {
	tempDir := t.TempDir()

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	if len(rfs.fileEntries) != 0 {
		t.Errorf("Expected 0 file entries, got %d", len(rfs.fileEntries))
	}

	if len(rfs.directories) != 0 && len(rfs.directories[""]) != 0 {
		t.Errorf("Expected empty directories, got %d", len(rfs.directories[""]))
	}
}

func TestNewRarFSWithSubdirectory(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory (simulating the expected structure)
	subDir := filepath.Join(tempDir, "movies")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// The subdirectory should be registered
	found := false
	for _, d := range rfs.directories[""] {
		if d == "movies" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'movies' directory to be registered")
	}
}

func TestFileEntry(t *testing.T) {
	entry := &FileEntry{
		Name:         "test/file.txt",
		Size:         1024,
		ModTime:      1234567890,
		IsDir:        false,
		ArchivePath:  "/path/to/archive.rar",
		InternalPath: "file.txt",
	}

	if entry.Name != "test/file.txt" {
		t.Errorf("Expected Name to be 'test/file.txt', got %q", entry.Name)
	}
	if entry.Size != 1024 {
		t.Errorf("Expected Size to be 1024, got %d", entry.Size)
	}
	if entry.IsDir {
		t.Error("Expected IsDir to be false")
	}
}

func TestNewRarFSWithNestedSubdirectories(t *testing.T) {
	tempDir := t.TempDir()

	// Create nested subdirectories (simulating deep directory structure)
	level1 := filepath.Join(tempDir, "media")
	level2 := filepath.Join(level1, "movies")
	level3 := filepath.Join(level2, "action")
	if err := os.MkdirAll(level3, 0755); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Check that 'media' is in root
	found := false
	for _, d := range rfs.directories[""] {
		if d == "media" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'media' directory to be registered in root")
	}

	// Check that 'movies' is in 'media'
	found = false
	for _, d := range rfs.directories["media"] {
		if d == "movies" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'movies' directory to be registered in 'media'")
	}

	// Check that 'action' is in 'media/movies'
	found = false
	for _, d := range rfs.directories["media/movies"] {
		if d == "action" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'action' directory to be registered in 'media/movies'")
	}
}

func TestEnsureDirectoryPath(t *testing.T) {
	rfs := &RarFS{
		fileEntries: make(map[string]*FileEntry),
		directories: make(map[string][]string),
	}

	rfs.ensureDirectoryPath("a/b/c")

	// Check 'a' is in root
	if len(rfs.directories[""]) != 1 || rfs.directories[""][0] != "a" {
		t.Errorf("Expected 'a' in root, got %v", rfs.directories[""])
	}

	// Check 'b' is in 'a'
	if len(rfs.directories["a"]) != 1 || rfs.directories["a"][0] != "b" {
		t.Errorf("Expected 'b' in 'a', got %v", rfs.directories["a"])
	}

	// Check 'c' is in 'a/b'
	if len(rfs.directories["a/b"]) != 1 || rfs.directories["a/b"][0] != "c" {
		t.Errorf("Expected 'c' in 'a/b', got %v", rfs.directories["a/b"])
	}
}

func TestAddToDirectory(t *testing.T) {
	rfs := &RarFS{
		fileEntries: make(map[string]*FileEntry),
		directories: make(map[string][]string),
	}

	// Add item
	rfs.addToDirectory("parent", "child1")
	if len(rfs.directories["parent"]) != 1 {
		t.Errorf("Expected 1 item, got %d", len(rfs.directories["parent"]))
	}

	// Add same item again (should be deduplicated)
	rfs.addToDirectory("parent", "child1")
	if len(rfs.directories["parent"]) != 1 {
		t.Errorf("Expected 1 item after duplicate add, got %d", len(rfs.directories["parent"]))
	}

	// Add different item
	rfs.addToDirectory("parent", "child2")
	if len(rfs.directories["parent"]) != 2 {
		t.Errorf("Expected 2 items, got %d", len(rfs.directories["parent"]))
	}
}
