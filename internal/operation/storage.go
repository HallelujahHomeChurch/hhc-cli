package operation

import (
	"os"
	"path/filepath"
	"strings"
)

// FinalizeDirectory atomically publishes an owned sibling staging directory.
// No overwrite fallback is allowed, including an existing empty user directory.
func FinalizeDirectory(staging, output string) error {
	if !filepath.IsAbs(staging) || !filepath.IsAbs(output) || filepath.Dir(staging) != filepath.Dir(output) || !strings.HasPrefix(filepath.Base(staging), ".hhc-prepare-") {
		return os.ErrInvalid
	}
	info, err := os.Lstat(staging)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return os.ErrInvalid
	}
	return finalizeDirectory(staging, output)
}

func AvailableBytes(directory string) (uint64, error) {
	if !filepath.IsAbs(directory) {
		return 0, os.ErrInvalid
	}
	info, err := os.Stat(directory)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, os.ErrInvalid
	}
	return availableBytes(directory)
}
