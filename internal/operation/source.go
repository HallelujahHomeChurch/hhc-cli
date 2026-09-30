package operation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrSourceChanged = errors.New("source_changed")
var ErrSourceSnapshotUnavailable = errors.New("source_snapshot_unavailable")

// OpenStableSource never modifies the original. The caller must keep the file
// open through encoding and owns the private workspace lifecycle. macOS clones
// remain in that workspace until the operation's remote-ready cleanup boundary.
func OpenStableSource(ctx context.Context, source, workspace string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(workspace) || source == workspace {
		return nil, os.ErrInvalid
	}
	switch strings.ToLower(filepath.Ext(source)) {
	case ".mp4", ".mkv", ".mov":
	default:
		return nil, os.ErrInvalid
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, os.ErrInvalid
	}
	dir, err := os.Lstat(workspace)
	if err != nil {
		return nil, err
	}
	if !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrInvalid
	}
	return openStableSource(ctx, source, workspace, info)
}
