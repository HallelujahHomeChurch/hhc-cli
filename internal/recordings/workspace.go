package recordings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Disposable paths are derived from immutable intent and the locked operation ID.
// No remote input, source path or journal-supplied path selects a delete target.
func (j *Journal) workspaceLocation() (string, string, error) {
	if j.lock == nil || j.state.Intent.Command != "prepare" && (j.state.Intent.Command != "upload" || !j.state.Intent.Prepare) {
		return "", "", ErrOperationConflict
	}
	parent, name := j.directory, "generated"
	if j.state.Intent.Command == "prepare" {
		parent, name = filepath.Dir(j.state.Intent.Output), ".hhc-prepare-"+j.state.OperationID
	}
	directory := filepath.Join(parent, name)
	rel, err := filepath.Rel(directory, j.state.Intent.Input)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) || directory == j.state.Intent.Output {
		return "", "", ErrOperationConflict
	}
	return parent, name, nil
}

func (j *Journal) generatedWorkspace() (string, error) {
	parent, name, err := j.workspaceLocation()
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
	directory := filepath.Join(parent, name)
	info, statErr := root.Lstat(name)
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
	if err := root.Mkdir(name, 0700); err != nil {
		return "", err
	}
	return directory, nil
}

func (j *Journal) cleanGenerated() error {
	if !j.state.GeneratedOwned {
		return ErrOperationConflict
	}
	parent, name, err := j.workspaceLocation()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrOperationConflict
	}
	return root.RemoveAll(name)
}
