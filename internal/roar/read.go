package roar

import (
	"archive/zip"
	"io"
	"os"

	"github.com/nwaples/rardecode/v2"
)

func (h *RarFileHandle) ReadAt(dest []byte, off int64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entry.IsPassthrough {
		return readPassthroughFileRange(h.entry.SourcePath, off, int64(len(dest)))
	}
	if isZipFile(h.entry.ArchivePath) {
		return extractZipFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
	}
	return extractFileRange(h.entry.ArchivePath, h.entry.InternalPath, off, int64(len(dest)))
}

func readPassthroughFileRange(sourcePath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, err
	}
	result := make([]byte, length)
	n, err := io.ReadFull(file, result)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
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

func extractZipFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if normalizeArchivePath(f.Name) != internalPath {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, rc, offset); err != nil && err != io.EOF {
				return nil, err
			}
		}
		result := make([]byte, length)
		n, err := io.ReadFull(rc, result)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
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
	return nil, os.ErrNotExist
}

func extractFileRange(archivePath, internalPath string, offset, length int64) ([]byte, error) {
	if length > maxReadSize {
		length = maxReadSize
	}
	reader, err := rardecode.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if normalizeArchivePath(header.Name) != internalPath {
			continue
		}
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, reader, offset); err != nil && err != io.EOF {
				return nil, err
			}
		}
		result := make([]byte, length)
		n, err := io.ReadFull(reader, result)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
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
	return nil, os.ErrNotExist
}
