package roar

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (r *RarFS) resolveArchivePath(relDir string) (string, string, bool, error) {
	relDir = strings.TrimPrefix(filepath.ToSlash(relDir), ".")
	relDir = strings.TrimPrefix(relDir, "/")
	if relDir == "" {
		return "", "", false, nil
	}

	segments := strings.Split(relDir, "/")
	baseDir := r.sourceDir
	for i, segment := range segments {
		candidateDir := filepath.Join(baseDir, segment)
		if info, err := os.Stat(candidateDir); err == nil && info.IsDir() {
			baseDir = candidateDir
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return "", "", false, err
		}

		archivePath, found, err := findArchiveFile(baseDir, segment)
		if err != nil {
			return "", "", false, err
		}
		if found {
			internalPath := strings.Join(segments[i+1:], "/")
			internalPath = strings.TrimPrefix(strings.TrimPrefix(internalPath, "/"), "/")
			return archivePath, internalPath, true, nil
		}

		return "", "", false, nil
	}

	return "", "", false, nil
}

func findArchiveFile(baseDir, archiveName string) (string, bool, error) {
	candidates := []string{
		filepath.Join(baseDir, archiveName+".zip"),
		filepath.Join(baseDir, archiveName+".rar"),
		filepath.Join(baseDir, archiveName+".part1.rar"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func (r *RarFS) listDirectoryEntries(relDir string) ([]dirEntry, bool, error) {
	relDir = strings.TrimPrefix(filepath.ToSlash(relDir), ".")
	relDir = strings.TrimPrefix(relDir, "/")
	entries := make(map[string]*dirEntry)

	archivePath, internalPrefix, isArchive, err := r.resolveArchivePath(relDir)
	if err != nil {
		return nil, false, err
	}
	if isArchive {
		dirExists, err := r.addArchiveDirectoryEntries(archivePath, internalPrefix, relDir, entries)
		if err != nil {
			return nil, false, err
		}
		if !dirExists {
			return nil, false, nil
		}
		result := make([]dirEntry, 0, len(entries))
		for _, entry := range entries {
			result = append(result, *entry)
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].name < result[j].name
		})
		return result, true, nil
	}

	absDir := filepath.Join(r.sourceDir, relDir)
	dirExists := false

	if info, err := os.Stat(absDir); err == nil && info.IsDir() {
		dirExists = true
		if err := r.addActualSourceDirectoryEntries(absDir, relDir, entries); err != nil {
			return nil, false, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}

	rarFound, err := r.addArchiveVirtualDirectories(absDir, relDir, entries)
	if err != nil {
		return nil, false, err
	}
	if rarFound {
		dirExists = true
	}

	if !dirExists {
		return nil, false, nil
	}

	result := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].name < result[j].name
	})
	return result, true, nil
}

func (r *RarFS) addArchiveDirectoryEntries(archivePath, internalPrefix, relDir string, entries map[string]*dirEntry) (bool, error) {
	var archiveEntries []*FileEntry
	var err error
	if isZipFile(archivePath) {
		archiveEntries, err = scanZipArchive(archivePath)
	} else {
		archiveEntries, err = scanArchive(archivePath)
	}
	if err != nil {
		return false, err
	}

	prefix := strings.TrimPrefix(strings.TrimPrefix(internalPrefix, "/"), "/")
	dirExists := false
	for _, file := range archiveEntries {
		path := file.InternalPath
		if prefix != "" {
			if path == prefix {
				if file.IsDir {
					dirExists = true
				}
				continue
			}
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			path = strings.TrimPrefix(path, prefix+"/")
		}

		parts := strings.Split(path, "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		name := parts[0]
		isDir := len(parts) > 1 || file.IsDir
		entry, ok := entries[name]
		if ok {
			entry.isDir = entry.isDir || isDir
			dirExists = true
			continue
		}
		var fileEntry *FileEntry
		if !isDir {
			fileEntry = &FileEntry{
				Name:         filepath.Join(relDir, name),
				Size:         file.Size,
				ModTime:      file.ModTime,
				IsDir:        false,
				ArchivePath:  archivePath,
				InternalPath: file.InternalPath,
			}
		}
		entries[name] = &dirEntry{name: name, isDir: isDir, file: fileEntry}
		dirExists = true
	}
	return dirExists, nil
}

func (r *RarFS) addActualSourceDirectoryEntries(absDir, relDir string, entries map[string]*dirEntry) error {
	items, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	for _, item := range items {
		name := item.Name()
		if item.IsDir() {
			entries[name] = &dirEntry{name: name, isDir: true}
			continue
		}
		if isRarFile(name) && isFirstRarPart(name) {
			archiveName := archiveBaseName(name)
			entries[archiveName] = &dirEntry{name: archiveName, isDir: true}
			continue
		}
		if isZipFile(name) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			logger.Warn("error reading pass-through file info", "file", name, "error", err)
			continue
		}
		entries[name] = &dirEntry{
			name:  name,
			isDir: false,
			file: &FileEntry{
				Name:          filepath.Join(relDir, name),
				Size:          info.Size(),
				ModTime:       info.ModTime().Unix(),
				IsDir:         false,
				IsPassthrough: true,
				SourcePath:    filepath.Join(absDir, name),
			},
		}
	}
	return nil
}

func (r *RarFS) addArchiveVirtualDirectories(absDir, relDir string, entries map[string]*dirEntry) (bool, error) {
	var found bool
	zipArchives, err := findZipArchives(absDir)
	if err != nil {
		return false, err
	}
	for _, archivePath := range zipArchives {
		name := archiveBaseName(filepath.Base(archivePath))
		entries[name] = &dirEntry{name: name, isDir: true}
		found = true
	}
	rarArchives, err := findRarArchives(absDir)
	if err != nil {
		return false, err
	}
	for _, archivePath := range rarArchives {
		name := archiveBaseName(filepath.Base(archivePath))
		entries[name] = &dirEntry{name: name, isDir: true}
		found = true
	}
	return found, nil
}

func (r *RarFS) lookupInDirectory(relDir, name string) (bool, *FileEntry, bool, error) {
	entries, exists, err := r.listDirectoryEntries(relDir)
	if err != nil || !exists {
		return false, nil, false, err
	}
	for _, entry := range entries {
		if entry.name == name {
			return entry.isDir, entry.file, true, nil
		}
	}
	return false, nil, false, nil
}
