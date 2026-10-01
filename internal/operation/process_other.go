//go:build !darwin && !linux && !windows

package operation

import (
	"context"
	"io"
	"os"
)

func runTool(context.Context, string, []string, io.Writer) error { return os.ErrInvalid }
