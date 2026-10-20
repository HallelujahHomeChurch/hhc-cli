//go:build !windows

package operation

import (
	"errors"
	"os"
	"path/filepath"
)

func openWorkspaceLock(directory string) (*os.File, error) {
	if !filepath.IsAbs(directory) {
		return nil, os.ErrInvalid
	}
	dir, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrInvalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(".lock")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	pathInfo, err := root.Lstat(".lock")
	if err != nil || !pathInfo.Mode().IsRegular() {
		f.Close()
		return nil, os.ErrInvalid
	}
	fileInfo, err := f.Stat()
	if err != nil || !os.SameFile(fileInfo, pathInfo) {
		f.Close()
		return nil, os.ErrInvalid
	}
	return f, nil
}
