package recordings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The only disposable tree is this fixed child of a locked operation journal.
// No remote input, source path or journal-supplied path selects a delete target.
func (j *Journal) generatedWorkspace() (string, error) {
	if j.lock == nil || j.state.Intent.Command != "upload" || !j.state.Intent.Prepare {
		return "", ErrOperationConflict
	}
	directory := filepath.Join(j.directory, "generated")
	rel, err := filepath.Rel(directory, j.state.Intent.Input)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOperationConflict
	}
	info, statErr := j.root.Lstat("generated")
	if !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil || !j.state.GeneratedOwned || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrOperationConflict
		}
		return directory, nil
	}
	state := j.State()
	state.GeneratedOwned = true
	if err := j.Save(state); err != nil {
		return "", err
	}
	if err := j.root.Mkdir("generated", 0700); err != nil {
		return "", err
	}
	return directory, nil
}

func (j *Journal) cleanGenerated() error {
	if j.lock == nil || !j.state.GeneratedOwned || !j.state.Intent.Prepare {
		return ErrOperationConflict
	}
	info, err := j.root.Lstat("generated")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrOperationConflict
	}
	return j.root.RemoveAll("generated")
}
