package roar

// Package roar implementation has been split across separate files:
// - types.go   : core filesystem types and shared constants
// - archive.go : archive detection and scanning helpers
// - directory.go : directory listing and archive path resolution
// - fuse.go    : FUSE node implementations
// - read.go    : file-range extraction and passthrough reads
// - mount.go   : mounting and cleanup helpers
