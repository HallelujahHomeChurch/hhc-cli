//go:build !windows && !darwin

package operation

import (
	"context"
	"os"
)

func openStableSource(context.Context, string, string, os.FileInfo) (*os.File, error) {
	return nil, ErrSourceSnapshotUnavailable
}
