package operation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openStableSource(ctx context.Context, source, workspace string, before os.FileInfo) (*os.File, error) {
	// mkdir is exclusive: never replace an existing operation snapshot.
	directory := filepath.Join(workspace, ".source")
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	snapshot := filepath.Join(directory, "recording"+filepath.Ext(source))
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(snapshot)
			_ = os.Remove(directory)
		}
	}()
	// cp -c silently falls back on EXDEV/ENOTSUP. Direct clonefile must fail
	// instead of allocating a potentially 50 GB second copy.
	if err := unix.Clonefile(source, snapshot, unix.CLONE_NOFOLLOW); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrSourceSnapshotUnavailable, err)
	}
	after, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrSourceChanged
	}
	if err := os.Chmod(snapshot, 0400); err != nil {
		return nil, err
	}
	f, err := os.Open(snapshot)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != before.Size() {
		f.Close()
		return nil, ErrSourceChanged
	}
	if err := ctx.Err(); err != nil {
		f.Close()
		return nil, err
	}
	keep = true
	return f, nil
}
