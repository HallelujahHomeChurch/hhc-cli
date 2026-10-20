package operation

import (
	"errors"
	"os"
)

var ErrOperationBusy = errors.New("operation_busy")

// LockWorkspace holds a non-blocking native lock until Close. Never unlink the
// lock file while the workspace exists: another process may hold its inode.
func LockWorkspace(directory string) (*os.File, error) {
	return lockDirectory(directory, false)
}

// LockSharedWorkspace allows concurrent commands while excluding an installer.
// It uses the same persistent lock inode as LockWorkspace.
func LockSharedWorkspace(directory string) (*os.File, error) {
	return lockDirectory(directory, true)
}

func lockDirectory(directory string, shared bool) (*os.File, error) {
	f, err := openWorkspaceLock(directory)
	if err != nil {
		return nil, err
	}
	if err := lockWorkspace(f, shared); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
