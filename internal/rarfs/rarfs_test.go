package rarfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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
		{"new style part1", "archive.part1.rar", true},
		{"new style part2", "archive.part2.rar", false},
		{"new style part10", "archive.part10.rar", false},
		{"uppercase part1", "ARCHIVE.PART1.RAR", true},
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

func TestFindPassthroughFiles(t *testing.T) {
	// Create a temporary directory structure
	tempDir := t.TempDir()

	// Create test files - a mix of RAR and non-RAR files
	rarFile := filepath.Join(tempDir, "test.rar")
	if err := os.WriteFile(rarFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	splitFile := filepath.Join(tempDir, "split.r01")
	if err := os.WriteFile(splitFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Non-RAR files that should be passed through
	txtFile := filepath.Join(tempDir, "readme.txt")
	if err := os.WriteFile(txtFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	nfoFile := filepath.Join(tempDir, "info.txt")
	if err := os.WriteFile(nfoFile, []byte("info"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := findPassthroughFiles(tempDir)
	if err != nil {
		t.Fatalf("findPassthroughFiles failed: %v", err)
	}

	// Should find 2 pass-through files (readme.txt and info.txt)
	if len(files) != 2 {
		t.Errorf("Expected 2 pass-through files, got %d", len(files))
	}

	// Check that the files are marked as pass-through
	for _, f := range files {
		if !f.IsPassthrough {
			t.Errorf("Expected file %s to be marked as pass-through", f.Name)
		}
		if f.SourcePath == "" {
			t.Errorf("Expected file %s to have a source path", f.Name)
		}
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

	// Check that 'media' is in root (discovered immediately at startup)
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

	// Trigger lazy discovery of 'media' directory contents
	rfs.ensureDirScanned("media")

	// Check that 'movies' is in 'media' (after lazy discovery)
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

	// Trigger lazy discovery of 'media/movies' directory contents
	rfs.ensureDirScanned("media/movies")

	// Check that 'action' is in 'media/movies' (after lazy discovery)
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
		fileEntries:  make(map[string]*FileEntry),
		directories:  make(map[string][]string),
		pathToInode:  make(map[string]uint64),
		inodeCounter: 1,
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
		fileEntries:  make(map[string]*FileEntry),
		directories:  make(map[string][]string),
		pathToInode:  make(map[string]uint64),
		inodeCounter: 1,
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

// TestScanArchiveOldStyle tests scanning old-style split archives (.rar, .r00, .r01)
func TestScanArchiveOldStyle(t *testing.T) {
	archivePath := filepath.Join("..", "..", "tests", "data", "example-1", "split.rar")

	// Check if test data exists
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	entries, err := scanArchive(archivePath)
	if err != nil {
		t.Fatalf("scanArchive failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one file in archive")
	}

	// Check that we found the expected file
	found := false
	for _, entry := range entries {
		if entry.Name == "complete" {
			found = true
			if entry.Size != 2048 {
				t.Errorf("Expected file size 2048, got %d", entry.Size)
			}
			if entry.IsPassthrough {
				t.Error("Archive file should not be marked as pass-through")
			}
		}
	}
	if !found {
		t.Error("Expected to find 'complete' file in archive")
	}
}

// TestScanArchiveNewStyle tests scanning new-style split archives (.part1.rar, .part2.rar)
func TestScanArchiveNewStyle(t *testing.T) {
	archivePath := filepath.Join("..", "..", "tests", "data", "example-2", "split.part1.rar")

	// Check if test data exists
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	entries, err := scanArchive(archivePath)
	if err != nil {
		t.Fatalf("scanArchive failed: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one file in archive")
	}

	// Check that we found the expected file
	found := false
	for _, entry := range entries {
		if entry.Name == "complete" {
			found = true
			if entry.Size != 2048 {
				t.Errorf("Expected file size 2048, got %d", entry.Size)
			}
		}
	}
	if !found {
		t.Error("Expected to find 'complete' file in archive")
	}
}

// TestPassthroughFilesWithTestData tests pass-through functionality with actual test data
func TestPassthroughFilesWithTestData(t *testing.T) {
	example1Path := filepath.Join("..", "..", "tests", "data", "example-1")

	// Check if test data exists
	if _, err := os.Stat(example1Path); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	files, err := findPassthroughFiles(example1Path)
	if err != nil {
		t.Fatalf("findPassthroughFiles failed: %v", err)
	}

	// Should find readme.txt as a pass-through file
	found := false
	for _, f := range files {
		if f.Name == "readme.txt" {
			found = true
			if !f.IsPassthrough {
				t.Error("Expected readme.txt to be marked as pass-through")
			}
			if f.SourcePath == "" {
				t.Error("Expected readme.txt to have a source path")
			}
			if f.Size == 0 {
				t.Error("Expected readme.txt to have non-zero size")
			}
		}
	}
	if !found {
		t.Error("Expected to find readme.txt as pass-through file")
	}
}

// TestNewRarFSWithTestData tests the full filesystem scan with actual test data
func TestNewRarFSWithTestData(t *testing.T) {
	testDataPath := filepath.Join("..", "..", "tests", "data")

	// Check if test data exists
	if _, err := os.Stat(testDataPath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	rfs, err := NewRarFS(testDataPath)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Check that we have directories for example-1 and example-2
	foundExample1 := false
	foundExample2 := false
	for _, d := range rfs.directories[""] {
		if d == "example-1" {
			foundExample1 = true
		}
		if d == "example-2" {
			foundExample2 = true
		}
	}
	if !foundExample1 {
		t.Error("Expected 'example-1' directory to be registered")
	}
	if !foundExample2 {
		t.Error("Expected 'example-2' directory to be registered")
	}

	// Trigger lazy scanning for both directories
	rfs.ensureDirScanned("example-1")
	rfs.ensureDirScanned("example-2")

	// Check for files from archives (now available after lazy scan)
	if _, ok := rfs.fileEntries["example-1/complete"]; !ok {
		t.Error("Expected 'example-1/complete' file from RAR archive")
	}
	if _, ok := rfs.fileEntries["example-2/complete"]; !ok {
		t.Error("Expected 'example-2/complete' file from RAR archive")
	}

	// Check for pass-through files
	if entry, ok := rfs.fileEntries["example-1/readme.txt"]; !ok {
		t.Error("Expected 'example-1/readme.txt' pass-through file")
	} else {
		if !entry.IsPassthrough {
			t.Error("Expected readme.txt to be marked as pass-through")
		}
	}

	if entry, ok := rfs.fileEntries["example-2/info.txt"]; !ok {
		t.Error("Expected 'example-2/info.txt' pass-through file")
	} else {
		if !entry.IsPassthrough {
			t.Error("Expected info.txt to be marked as pass-through")
		}
	}
}

// TestReadPassthroughFile tests reading content from a pass-through file
func TestReadPassthroughFile(t *testing.T) {
	testFilePath := filepath.Join("..", "..", "tests", "data", "example-1", "readme.txt")

	// Check if test data exists
	if _, err := os.Stat(testFilePath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	// Read using the pass-through function
	data, err := readPassthroughFileRange(testFilePath, 0, 100)
	if err != nil {
		t.Fatalf("readPassthroughFileRange failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected non-empty data from pass-through file")
	}

	// Verify content starts with expected text
	expectedPrefix := "This is a readme"
	if len(data) < len(expectedPrefix) {
		t.Errorf("Expected data to be at least %d bytes, got %d bytes: %q", len(expectedPrefix), len(data), string(data))
	} else if string(data[:len(expectedPrefix)]) != expectedPrefix {
		t.Errorf("Expected data to start with %q, got %q", expectedPrefix, string(data[:len(expectedPrefix)]))
	}
}

// TestExtractFileFromArchive tests extracting content from a RAR archive
func TestExtractFileFromArchive(t *testing.T) {
	archivePath := filepath.Join("..", "..", "tests", "data", "example-1", "split.rar")

	// Check if test data exists
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	// Extract some data from the archive
	data, err := extractFileRange(archivePath, "complete", 0, 100)
	if err != nil {
		t.Fatalf("extractFileRange failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected non-empty data from archive")
	}

	if len(data) != 100 {
		t.Errorf("Expected 100 bytes, got %d", len(data))
	}
}

// TestLazyScanning tests that RAR archives are only scanned when directories are accessed
func TestLazyScanning(t *testing.T) {
	testDataPath := filepath.Join("..", "..", "tests", "data")

	// Check if test data exists
	if _, err := os.Stat(testDataPath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	rfs, err := NewRarFS(testDataPath)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Initially, directories should NOT be scanned (files from RAR archives not present)
	if _, ok := rfs.fileEntries["example-1/complete"]; ok {
		t.Error("Files from RAR archives should not be present before directory access")
	}

	// Directories should be pending
	if _, ok := rfs.pendingDirs["example-1"]; !ok {
		t.Error("example-1 should be in pendingDirs")
	}

	// scannedDirs should be empty for example-1
	if rfs.scannedDirs["example-1"] {
		t.Error("example-1 should not be marked as scanned yet")
	}

	// Trigger lazy scan for example-1
	rfs.ensureDirScanned("example-1")

	// Now the files should be present
	if _, ok := rfs.fileEntries["example-1/complete"]; !ok {
		t.Error("Files from RAR archives should be present after lazy scan")
	}

	// scannedDirs should now have example-1
	if !rfs.scannedDirs["example-1"] {
		t.Error("example-1 should be marked as scanned after ensureDirScanned")
	}

	// example-2 should still be unscanned
	if rfs.scannedDirs["example-2"] {
		t.Error("example-2 should not be marked as scanned yet")
	}
	if _, ok := rfs.fileEntries["example-2/complete"]; ok {
		t.Error("Files from example-2 RAR archives should not be present yet")
	}
}

// TestStableInodes tests that inode numbers are stable and consistent
func TestStableInodes(t *testing.T) {
	rfs := &RarFS{
		fileEntries:    make(map[string]*FileEntry),
		directories:    make(map[string][]string),
		discoveredDirs: make(map[string]bool),
		pathToInode:    make(map[string]uint64),
		inodeCounter:   1,
	}

	// Add directories and check inodes are assigned
	rfs.addToDirectory("", "dir1")
	rfs.addToDirectory("", "dir2")
	rfs.addToDirectory("dir1", "file1")

	// Verify inodes were assigned
	ino1 := rfs.getInode("dir1")
	ino2 := rfs.getInode("dir2")
	ino3 := rfs.getInode("dir1/file1")

	if ino1 == 0 || ino2 == 0 || ino3 == 0 {
		t.Error("Expected non-zero inodes to be assigned")
	}

	// Verify inodes are unique
	if ino1 == ino2 || ino1 == ino3 || ino2 == ino3 {
		t.Error("Expected inodes to be unique")
	}

	// Verify inodes are stable (calling getInode again returns same value)
	if rfs.getInode("dir1") != ino1 {
		t.Error("Expected inode to be stable")
	}
}

// TestLazyDirectoryDiscovery tests that subdirectories are only discovered when parent directories are accessed
func TestLazyDirectoryDiscovery(t *testing.T) {
	tempDir := t.TempDir()

	// Create a nested directory structure
	level1 := filepath.Join(tempDir, "level1")
	level2 := filepath.Join(level1, "level2")
	level3 := filepath.Join(level2, "level3")
	if err := os.MkdirAll(level3, 0755); err != nil {
		t.Fatal(err)
	}

	// Also create a pass-through file in level2
	passthrough := filepath.Join(level2, "file.txt")
	if err := os.WriteFile(passthrough, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Initially, only level1 should be in root (discovered at startup)
	if len(rfs.directories[""]) != 1 {
		t.Errorf("Expected 1 directory in root at startup, got %d", len(rfs.directories[""]))
	}
	if rfs.directories[""][0] != "level1" {
		t.Errorf("Expected 'level1' in root, got %v", rfs.directories[""])
	}

	// level2 should NOT be discovered yet
	if len(rfs.directories["level1"]) != 0 {
		t.Errorf("Expected 0 items in level1 before discovery, got %d", len(rfs.directories["level1"]))
	}

	// level1 should NOT be marked as discovered
	if rfs.discoveredDirs["level1"] {
		t.Error("level1 should not be marked as discovered yet")
	}

	// Trigger lazy discovery of level1
	rfs.ensureDirScanned("level1")

	// Now level2 should be discovered
	if len(rfs.directories["level1"]) != 1 {
		t.Errorf("Expected 1 item in level1 after discovery, got %d", len(rfs.directories["level1"]))
	}
	if rfs.directories["level1"][0] != "level2" {
		t.Errorf("Expected 'level2' in level1, got %v", rfs.directories["level1"])
	}

	// level1 should now be marked as discovered
	if !rfs.discoveredDirs["level1"] {
		t.Error("level1 should be marked as discovered after ensureDirScanned")
	}

	// level3 should NOT be discovered yet (we didn't access level2)
	if len(rfs.directories["level1/level2"]) != 0 {
		t.Errorf("Expected 0 items in level1/level2 before discovery, got %d", len(rfs.directories["level1/level2"]))
	}

	// Trigger lazy discovery of level2
	rfs.ensureDirScanned("level1/level2")

	// Check that level3 directory was discovered
	foundLevel3 := false
	for _, name := range rfs.directories["level1/level2"] {
		if name == "level3" {
			foundLevel3 = true
			break
		}
	}
	if !foundLevel3 {
		t.Error("Expected 'level3' directory to be discovered in level1/level2")
	}

	// Check that the pass-through file was discovered
	if _, ok := rfs.fileEntries["level1/level2/file.txt"]; !ok {
		t.Error("Expected pass-through file to be discovered in level1/level2")
	}
}

// TestConcurrentDirectoryScanning tests that multiple directories can be scanned concurrently
func TestConcurrentDirectoryScanning(t *testing.T) {
	testDataPath := filepath.Join("..", "..", "tests", "data")

	// Check if test data exists
	if _, err := os.Stat(testDataPath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	rfs, err := NewRarFS(testDataPath)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Initially, both directories should NOT be scanned
	if rfs.scannedDirs["example-1"] {
		t.Error("example-1 should not be scanned yet")
	}
	if rfs.scannedDirs["example-2"] {
		t.Error("example-2 should not be scanned yet")
	}

	// Scan both directories concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rfs.ensureDirScanned("example-1")
	}()

	go func() {
		defer wg.Done()
		rfs.ensureDirScanned("example-2")
	}()

	wg.Wait()

	// Both directories should now be scanned
	if !rfs.scannedDirs["example-1"] {
		t.Error("example-1 should be scanned after concurrent scan")
	}
	if !rfs.scannedDirs["example-2"] {
		t.Error("example-2 should be scanned after concurrent scan")
	}

	// Files from both archives should be available
	if _, ok := rfs.fileEntries["example-1/complete"]; !ok {
		t.Error("Expected 'example-1/complete' file from RAR archive")
	}
	if _, ok := rfs.fileEntries["example-2/complete"]; !ok {
		t.Error("Expected 'example-2/complete' file from RAR archive")
	}
}

// TestConcurrentSameDirectoryScanning tests that multiple goroutines scanning the same directory
// doesn't cause issues
func TestConcurrentSameDirectoryScanning(t *testing.T) {
	testDataPath := filepath.Join("..", "..", "tests", "data")

	// Check if test data exists
	if _, err := os.Stat(testDataPath); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	rfs, err := NewRarFS(testDataPath)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Scan the same directory from multiple goroutines concurrently
	var wg sync.WaitGroup
	numGoroutines := 10
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			rfs.ensureDirScanned("example-1")
		}()
	}

	wg.Wait()

	// Directory should be scanned exactly once
	if !rfs.scannedDirs["example-1"] {
		t.Error("example-1 should be scanned after concurrent scans")
	}

	// File should be available
	if _, ok := rfs.fileEntries["example-1/complete"]; !ok {
		t.Error("Expected 'example-1/complete' file from RAR archive")
	}
}

// TestFilesystemWatcher tests that the filesystem watcher detects changes
func TestFilesystemWatcher(t *testing.T) {
	tempDir := t.TempDir()

	// Create initial directory structure
	subDir := filepath.Join(tempDir, "watched")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create initial file
	initialFile := filepath.Join(subDir, "initial.txt")
	if err := os.WriteFile(initialFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() {
		_ = rfs.Close()
	}()

	// Trigger scan to populate cache
	rfs.ensureDirScanned("watched")

	// Verify initial file is present
	if _, ok := rfs.fileEntries["watched/initial.txt"]; !ok {
		t.Error("Expected initial.txt to be present")
	}

	// Add a new file to the watched directory
	newFile := filepath.Join(subDir, "newfile.txt")
	if err := os.WriteFile(newFile, []byte("new content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to process the event
	// Note: This is a simple approach; in production you might use channels or polling
	time.Sleep(100 * time.Millisecond)

	// The directory should be invalidated, so rescan it
	rfs.ensureDirScanned("watched")

	// Verify new file is now present
	if _, ok := rfs.fileEntries["watched/newfile.txt"]; !ok {
		t.Error("Expected newfile.txt to be present after filesystem change")
	}
}

// TestInvalidateDirectory tests that invalidating a directory clears its cache
func TestInvalidateDirectory(t *testing.T) {
	tempDir := t.TempDir()

	// Create directory with a file
	subDir := filepath.Join(tempDir, "testdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() {
		_ = rfs.Close()
	}()

	// Scan directory to populate cache
	rfs.ensureDirScanned("testdir")

	// Verify file is present
	if _, ok := rfs.fileEntries["testdir/test.txt"]; !ok {
		t.Error("Expected test.txt to be present before invalidation")
	}

	// Verify directory is marked as scanned
	if !rfs.scannedDirs["testdir"] {
		t.Error("Expected testdir to be marked as scanned")
	}

	// Invalidate the directory
	rfs.invalidateDirectory("testdir")

	// Verify cache is cleared
	if _, ok := rfs.fileEntries["testdir/test.txt"]; ok {
		t.Error("Expected test.txt to be removed from cache after invalidation")
	}

	// Verify directory is marked as not scanned
	if rfs.scannedDirs["testdir"] {
		t.Error("Expected testdir to be marked as not scanned after invalidation")
	}

	// Verify directory entries are cleared
	if _, ok := rfs.directories["testdir"]; ok {
		t.Error("Expected directory entries to be cleared after invalidation")
	}
}

// TestWatcherAddDirectory tests that directories are added to the watch list
func TestWatcherAddDirectory(t *testing.T) {
	tempDir := t.TempDir()

	// Create subdirectory
	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Check that the root directory is being watched
	if !rfs.watchedDirs[tempDir] {
		t.Error("Expected root directory to be watched")
	}

	// Trigger discovery of subdirectory
	rfs.ensureDirScanned("subdir")

	// Check that subdirectory is now being watched
	if !rfs.watchedDirs[subDir] {
		t.Error("Expected subdirectory to be watched after discovery")
	}
}

// TestWatcherClose tests that closing the filesystem properly shuts down the watcher
func TestWatcherClose(t *testing.T) {
	tempDir := t.TempDir()

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}

	// Close should not return an error
	if err := rfs.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}

	// Closing again should be safe (watcher is nil or already closed)
	if err := rfs.Close(); err != nil {
		t.Errorf("Second Close returned error: %v", err)
	}
}

// TestInvalidateDirectoryWithNestedFiles tests that invalidating a directory
// removes all nested files
func TestInvalidateDirectoryWithNestedFiles(t *testing.T) {
	rfs := &RarFS{
		fileEntries:    make(map[string]*FileEntry),
		directories:    make(map[string][]string),
		scannedDirs:    make(map[string]bool),
		discoveredDirs: make(map[string]bool),
		pathToInode:    make(map[string]uint64),
		inodeCounter:   1,
	}

	// Add some test entries
	rfs.fileEntries["dir1/file1.txt"] = &FileEntry{Name: "file1.txt"}
	rfs.fileEntries["dir1/file2.txt"] = &FileEntry{Name: "file2.txt"}
	rfs.fileEntries["dir1/subdir/file3.txt"] = &FileEntry{Name: "file3.txt"}
	rfs.fileEntries["dir2/file4.txt"] = &FileEntry{Name: "file4.txt"}
	rfs.scannedDirs["dir1"] = true
	rfs.discoveredDirs["dir1"] = true
	rfs.directories["dir1"] = []string{"file1.txt", "file2.txt", "subdir"}

	// Invalidate dir1
	rfs.invalidateDirectory("dir1")

	// Check that dir1 files are removed
	if _, ok := rfs.fileEntries["dir1/file1.txt"]; ok {
		t.Error("Expected dir1/file1.txt to be removed")
	}
	if _, ok := rfs.fileEntries["dir1/file2.txt"]; ok {
		t.Error("Expected dir1/file2.txt to be removed")
	}
	if _, ok := rfs.fileEntries["dir1/subdir/file3.txt"]; ok {
		t.Error("Expected dir1/subdir/file3.txt to be removed")
	}

	// Check that dir2 files are NOT removed
	if _, ok := rfs.fileEntries["dir2/file4.txt"]; !ok {
		t.Error("Expected dir2/file4.txt to still be present")
	}

	// Check that scannedDirs and discoveredDirs are cleared for dir1
	if rfs.scannedDirs["dir1"] {
		t.Error("Expected dir1 to be removed from scannedDirs")
	}
	if rfs.discoveredDirs["dir1"] {
		t.Error("Expected dir1 to be removed from discoveredDirs")
	}

	// Check that directory listing is cleared
	if _, ok := rfs.directories["dir1"]; ok {
		t.Error("Expected dir1 to be removed from directories")
	}
}

// TestHandleFileSystemEvent tests event handling logic
func TestHandleFileSystemEvent(t *testing.T) {
	tempDir := t.TempDir()

	// Create directory structure
	subDir := filepath.Join(tempDir, "testdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Scan directory to populate cache
	rfs.ensureDirScanned("testdir")

	// Verify directory is cached
	if !rfs.scannedDirs["testdir"] {
		t.Error("Expected testdir to be scanned")
	}

	// Simulate a write event in the testdir
	event := fsnotify.Event{
		Name: testFile,
		Op:   fsnotify.Write,
	}

	rfs.handleFileSystemEvent(event)

	// Verify the parent directory cache was invalidated
	if rfs.scannedDirs["testdir"] {
		t.Error("Expected testdir to be invalidated after write event")
	}
}

// TestWatcherWithRealRARFiles tests that changes to RAR files trigger rescanning
func TestWatcherWithRealRARFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create subdirectory
	subDir := filepath.Join(tempDir, "archives")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Copy a test RAR file to the watched directory
	srcRar := filepath.Join("..", "..", "tests", "data", "example-1", "split.rar")
	if _, err := os.Stat(srcRar); os.IsNotExist(err) {
		t.Skip("Test data not found, skipping")
	}

	dstRar := filepath.Join(subDir, "test.rar")

	// Read source RAR
	data, err := os.ReadFile(srcRar)
	if err != nil {
		t.Fatalf("Failed to read source RAR: %v", err)
	}

	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Initially, archives directory should have no RAR files
	rfs.ensureDirScanned("archives")

	initialFileCount := len(rfs.fileEntries)

	// Write the RAR file
	if err := os.WriteFile(dstRar, data, 0644); err != nil {
		t.Fatalf("Failed to write RAR file: %v", err)
	}

	// Also copy the split parts if they exist
	for i := 0; i < 2; i++ {
		srcPart := filepath.Join("..", "..", "tests", "data", "example-1", fmt.Sprintf("split.r%02d", i))
		if data, err := os.ReadFile(srcPart); err == nil {
			dstPart := filepath.Join(subDir, fmt.Sprintf("test.r%02d", i))
			if err := os.WriteFile(dstPart, data, 0644); err != nil {
				t.Logf("Warning: Failed to write split part %d: %v", i, err)
			}
		}
	}

	// Give the watcher time to detect the change
	time.Sleep(200 * time.Millisecond)

	// Rescan the directory
	rfs.ensureDirScanned("archives")

	// After adding the RAR file, we should have more files
	newFileCount := len(rfs.fileEntries)
	if newFileCount <= initialFileCount {
		t.Errorf("Expected file count to increase after adding RAR file, was %d, now %d",
			initialFileCount, newFileCount)
	}

	// Check that files from the RAR archive are now accessible
	foundComplete := false
	for path := range rfs.fileEntries {
		if strings.HasSuffix(path, "complete") {
			foundComplete = true
			break
		}
	}
	if !foundComplete {
		t.Error("Expected to find 'complete' file from RAR archive after filesystem change")
	}
}

// TestMountWithAllowOther tests that the Mount function accepts the allowOther parameter
func TestMountWithAllowOther(t *testing.T) {
	tempDir := t.TempDir()
	mountPoint := t.TempDir()

	// Test with allowOther = false
	server, rfs, err := Mount(tempDir, mountPoint, false)
	if err != nil {
		t.Fatalf("Mount with allowOther=false failed: %v", err)
	}
	if server == nil {
		t.Error("Expected server to be non-nil")
	}
	if rfs == nil {
		t.Error("Expected rfs to be non-nil")
	}

	// Clean up
	_ = server.Unmount()
	_ = rfs.Close()

	// Test with allowOther = true
	// Note: This may fail if user_allow_other is not set in /etc/fuse.conf
	// We'll just verify it doesn't crash
	mountPoint2 := t.TempDir()
	server2, rfs2, err2 := Mount(tempDir, mountPoint2, true)
	if err2 != nil {
		// If it fails, it might be due to /etc/fuse.conf settings
		// That's expected in many test environments
		t.Logf("Mount with allowOther=true failed (this may be expected): %v", err2)
	} else {
		if server2 == nil {
			t.Error("Expected server2 to be non-nil")
		}
		if rfs2 == nil {
			t.Error("Expected rfs2 to be non-nil")
		}
		_ = server2.Unmount()
		_ = rfs2.Close()
	}
}

// TestMountOptions tests that mount options are correctly set
func TestMountOptions(t *testing.T) {
	tempDir := t.TempDir()

	// Create a test filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Verify the filesystem was created successfully
	if rfs.sourceDir != tempDir {
		t.Errorf("Expected sourceDir to be %s, got %s", tempDir, rfs.sourceDir)
	}

	// Verify watcher was initialized
	if rfs.watcher == nil {
		t.Error("Expected watcher to be initialized")
	}

	// Verify maps were initialized
	if rfs.fileEntries == nil {
		t.Error("Expected fileEntries to be initialized")
	}
	if rfs.directories == nil {
		t.Error("Expected directories to be initialized")
	}
	if rfs.watchedDirs == nil {
		t.Error("Expected watchedDirs to be initialized")
	}
}

// TestReaddirTriggersRescan tests that Readdir triggers a rescan of the directory
func TestReaddirTriggersRescan(t *testing.T) {
	tempDir := t.TempDir()

	// Create initial directory with a file
	subDir := filepath.Join(tempDir, "testdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	initialFile := filepath.Join(subDir, "file1.txt")
	if err := os.WriteFile(initialFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Ensure the directory is discovered and scanned
	rfs.ensureDirDiscovered("testdir")
	rfs.ensureDirScanned("testdir")

	// Verify initial file is present
	rfs.mu.RLock()
	initialCount := len(rfs.fileEntries)
	hasFile1 := false
	for path := range rfs.fileEntries {
		if strings.Contains(path, "file1.txt") {
			hasFile1 = true
			break
		}
	}
	rfs.mu.RUnlock()

	if !hasFile1 {
		t.Error("Expected to find file1.txt in initial scan")
	}

	// Add a new file to the directory
	newFile := filepath.Join(subDir, "file2.txt")
	if err := os.WriteFile(newFile, []byte("test2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Give the filesystem a moment to settle
	time.Sleep(100 * time.Millisecond)

	// Invalidate the directory (simulating what Readdir does)
	rfs.invalidateDirectory("testdir")

	// Rediscover and rescan (simulating what Readdir does)
	rfs.ensureDirDiscovered("testdir")
	rfs.ensureDirScanned("testdir")

	// Verify the new file is now present
	rfs.mu.RLock()
	newCount := len(rfs.fileEntries)
	hasFile2 := false
	for path := range rfs.fileEntries {
		if strings.Contains(path, "file2.txt") {
			hasFile2 = true
			break
		}
	}
	rfs.mu.RUnlock()

	if !hasFile2 {
		t.Error("Expected to find file2.txt after rescan")
	}

	if newCount <= initialCount {
		t.Errorf("Expected more files after rescan, got %d initially and %d after", initialCount, newCount)
	}
}

// TestInvalidateDirectoryClearsCaches tests that invalidateDirectory properly clears all caches
func TestInvalidateDirectoryClearsCaches(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory with files
	subDir := filepath.Join(tempDir, "cachedir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Scan the directory
	rfs.ensureDirDiscovered("cachedir")
	rfs.ensureDirScanned("cachedir")

	// Verify directory is marked as scanned and discovered
	rfs.mu.RLock()
	if !rfs.scannedDirs["cachedir"] {
		t.Error("Expected cachedir to be marked as scanned")
	}
	if !rfs.discoveredDirs["cachedir"] {
		t.Error("Expected cachedir to be marked as discovered")
	}
	hasEntries := len(rfs.fileEntries) > 0
	rfs.mu.RUnlock()

	if !hasEntries {
		t.Error("Expected some file entries after scanning")
	}

	// Invalidate the directory
	rfs.invalidateDirectory("cachedir")

	// Verify caches are cleared
	rfs.mu.RLock()
	if rfs.scannedDirs["cachedir"] {
		t.Error("Expected cachedir scanned flag to be cleared")
	}
	if rfs.discoveredDirs["cachedir"] {
		t.Error("Expected cachedir discovered flag to be cleared")
	}

	// Check that file entries for this directory are removed
	hasCachedirEntries := false
	for path := range rfs.fileEntries {
		if strings.HasPrefix(path, "cachedir/") {
			hasCachedirEntries = true
			break
		}
	}
	rfs.mu.RUnlock()

	if hasCachedirEntries {
		t.Error("Expected cachedir file entries to be cleared after invalidation")
	}
}

// TestReaddirPicksUpNewFiles tests that repeated Readdir operations pick up new files
func TestReaddirPicksUpNewFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory
	subDir := filepath.Join(tempDir, "dynamic")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Initial scan
	rfs.ensureDirDiscovered("dynamic")
	rfs.ensureDirScanned("dynamic")

	rfs.mu.RLock()
	initialCount := len(rfs.fileEntries)
	rfs.mu.RUnlock()

	// Add a new file
	newFile := filepath.Join(subDir, "new.txt")
	if err := os.WriteFile(newFile, []byte("new content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait a bit for filesystem to settle
	time.Sleep(50 * time.Millisecond)

	// Invalidate and rescan (simulating Readdir)
	rfs.invalidateDirectory("dynamic")
	rfs.ensureDirDiscovered("dynamic")
	rfs.ensureDirScanned("dynamic")

	rfs.mu.RLock()
	finalCount := len(rfs.fileEntries)
	hasNewFile := false
	for path := range rfs.fileEntries {
		if strings.Contains(path, "new.txt") {
			hasNewFile = true
			break
		}
	}
	rfs.mu.RUnlock()

	if !hasNewFile {
		t.Error("Expected to find new.txt after rescan")
	}

	if finalCount <= initialCount {
		t.Errorf("Expected more files after adding new.txt: initial=%d, final=%d", initialCount, finalCount)
	}
}

// TestInvalidateDirectoryRestoresPendingDirs tests that invalidateDirectory
// properly restores pendingDirs entries so directories can be rediscovered
func TestInvalidateDirectoryRestoresPendingDirs(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory with a file
	subDir := filepath.Join(tempDir, "testdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subDir, "initial.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Initial scan
	rfs.ensureDirDiscovered("testdir")
	rfs.ensureDirScanned("testdir")

	// Verify initial state
	rfs.mu.RLock()
	if !rfs.discoveredDirs["testdir"] {
		t.Error("Expected testdir to be marked as discovered")
	}
	rfs.mu.RUnlock()

	// Invalidate the directory
	rfs.invalidateDirectory("testdir")

	// Verify cache is cleared but pendingDirs is restored
	rfs.mu.RLock()
	if rfs.discoveredDirs["testdir"] {
		t.Error("Expected testdir discovered flag to be cleared")
	}
	restoredPath := rfs.pendingDirs["testdir"]
	rfs.mu.RUnlock()

	if restoredPath == "" {
		t.Error("Expected pendingDirs entry to be restored after invalidation")
	}

	expectedPath := filepath.Join(tempDir, "testdir")
	if restoredPath != expectedPath {
		t.Errorf("Expected pendingDirs path to be %s, got %s", expectedPath, restoredPath)
	}

	// Now try to rediscover - should work because pendingDirs was restored
	rfs.ensureDirDiscovered("testdir")

	rfs.mu.RLock()
	rediscovered := rfs.discoveredDirs["testdir"]
	rfs.mu.RUnlock()

	if !rediscovered {
		t.Error("Expected testdir to be rediscovered after invalidation")
	}
}

// TestInvalidateRootDirectoryRestoresPendingDirs tests that invalidating
// the root directory properly restores its pendingDirs entry
func TestInvalidateRootDirectoryRestoresPendingDirs(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory in root
	subDir := filepath.Join(tempDir, "rootsub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create filesystem
	rfs, err := NewRarFS(tempDir)
	if err != nil {
		t.Fatalf("NewRarFS failed: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// Verify root is discovered initially
	rfs.mu.RLock()
	if !rfs.discoveredDirs[""] {
		t.Error("Expected root to be marked as discovered")
	}
	initialDirCount := len(rfs.directories[""])
	rfs.mu.RUnlock()

	if initialDirCount == 0 {
		t.Error("Expected root to have at least one subdirectory")
	}

	// Invalidate root directory
	rfs.invalidateDirectory("")

	// Verify cache is cleared
	rfs.mu.RLock()
	if rfs.discoveredDirs[""] {
		t.Error("Expected root discovered flag to be cleared")
	}
	if len(rfs.directories[""]) != 0 {
		t.Error("Expected root directory entries to be cleared")
	}
	restoredPath := rfs.pendingDirs[""]
	rfs.mu.RUnlock()

	if restoredPath == "" {
		t.Error("Expected root pendingDirs entry to be restored after invalidation")
	}

	if restoredPath != tempDir {
		t.Errorf("Expected root pendingDirs path to be %s, got %s", tempDir, restoredPath)
	}

	// Rediscover root - should work because pendingDirs was restored
	rfs.ensureDirDiscovered("")

	rfs.mu.RLock()
	rediscovered := rfs.discoveredDirs[""]
	newDirCount := len(rfs.directories[""])
	rfs.mu.RUnlock()

	if !rediscovered {
		t.Error("Expected root to be rediscovered after invalidation")
	}

	if newDirCount == 0 {
		t.Error("Expected root to have subdirectories after rediscovery")
	}

	if newDirCount != initialDirCount {
		t.Errorf("Expected same directory count after rediscovery: initial=%d, new=%d", initialDirCount, newDirCount)
	}
}
