package operation

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrOperationBusy = errors.New("operation_busy")

// LockWorkspace holds a non-blocking native lock until Close. Never unlink the
// lock file while the workspace exists: another process may hold its inode.
func LockWorkspace(directory string) (*os.File, error) {
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
	if err := lockWorkspace(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
