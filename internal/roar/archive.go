package roar

import (
	"archive/zip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

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
