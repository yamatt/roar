package roar

import (
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func Mount(sourceDir, mountPoint string, allowOther bool) (*fuse.Server, *RarFS, error) {
	logger.Info("mounting filesystem", "source", sourceDir, "mountPoint", mountPoint, "allowOther", allowOther)
	rfs, err := NewRarFS(sourceDir)
	if err != nil {
		logger.Error("failed to create filesystem", "error", err)
		return nil, nil, err
	}
	root := &RarFSRoot{rfs: rfs}
	opts := &fs.Options{MountOptions: fuse.MountOptions{AllowOther: allowOther, Debug: false, FsName: "roar", Name: "roar"}}
	server, err := fs.Mount(mountPoint, root, opts)
	if err != nil {
		logger.Error("failed to mount filesystem", "error", err)
		return nil, nil, err
	}
	logger.Info("filesystem mounted successfully")
	return server, rfs, nil
}

func (r *RarFS) Close() error {
	return nil
}
