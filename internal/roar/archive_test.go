package roar

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchivePureFunctions(t *testing.T) {
	cases := []struct {
		name       string
		isRar      bool
		isZip      bool
		isFirstRar bool
		base       string
	}{
		{"file.rar", true, false, true, "file"},
		{"file.part1.rar", true, false, true, "file"},
		{"file.r00", true, false, false, "file"},
		{"archive.zip", false, true, false, "archive"},
		{"archive.tar.gz", false, false, false, "archive.tar"},
	}

	for _, c := range cases {
		if isRarFile(c.name) != c.isRar {
			t.Fatalf("isRarFile(%s) expected %v", c.name, c.isRar)
		}
		if isZipFile(c.name) != c.isZip {
			t.Fatalf("isZipFile(%s) expected %v", c.name, c.isZip)
		}
		if isFirstRarPart(c.name) != c.isFirstRar {
			t.Fatalf("isFirstRarPart(%s) expected %v", c.name, c.isFirstRar)
		}
		if archiveBaseName(c.name) != c.base {
			t.Fatalf("archiveBaseName(%s) expected %s, got %s", c.name, c.base, archiveBaseName(c.name))
		}
	}

	if normalizeArchivePath("/a/b/") != "a/b" {
		t.Fatalf("normalizeArchivePath failed")
	}
}

func TestFindZipAndScanZip(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	wf, err := w.Create("dir/inside.txt")
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	if _, err := io.WriteString(wf, "hello"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if _, err := w.Create("root.txt"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	_ = w.Close()
	_ = f.Close()

	zips, err := findZipArchives(tmp)
	if err != nil {
		t.Fatalf("findZipArchives error: %v", err)
	}
	if len(zips) != 1 || zips[0] != zipPath {
		t.Fatalf("expected found zip %s, got %v", zipPath, zips)
	}

	entries, err := scanZipArchive(zipPath)
	if err != nil {
		t.Fatalf("scanZipArchive error: %v", err)
	}
	foundInside, foundRoot := false, false
	for _, e := range entries {
		if e.InternalPath == "dir/inside.txt" {
			foundInside = true
		}
		if e.InternalPath == "root.txt" {
			foundRoot = true
		}
	}
	if !foundInside || !foundRoot {
		t.Fatalf("expected both entries in zip, got %v", entries)
	}
}
