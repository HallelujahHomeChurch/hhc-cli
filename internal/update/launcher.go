package update

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

// Launch never evaluates a shell. The selected version is retained permanently;
// after startup the managed child holds its own shared installation lock.
func Launch(directory string, args []string, input *os.File, output, diagnostics io.Writer) (int, error) {
	lock, err := operation.LockSharedWorkspace(directory)
	if err != nil {
		return 5, err
	}
	p, err := ReadPointer(directory)
	if err != nil {
		lock.Close()
		return 5, err
	}
	versionDir := filepath.Join(directory, "versions", p.Current)
	name := "hhc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(versionDir, name)
	for _, path := range []string{filepath.Dir(versionDir), versionDir, binary} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (path == binary && !info.Mode().IsRegular()) || (path != binary && !info.IsDir()) {
			lock.Close()
			return 5, ErrInvalidRelease
		}
	}
	// Release before starting an update command, which needs the exclusive lock.
	// A concurrent switch is safe: old versions are retained and both versions
	// use the same lock inode and journal schema.
	lock.Close()
	command := exec.Command(binary, args...)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = diagnostics
	if err := command.Run(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return exited.ExitCode(), nil
		}
		return 5, ErrInvalidRelease
	}
	return 0, nil
}
