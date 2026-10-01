//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

func replacePointer(directory, temp string) error {
	if err := os.Rename(temp, filepath.Join(directory, "current.json")); err != nil {
		return err
	}
	f, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
