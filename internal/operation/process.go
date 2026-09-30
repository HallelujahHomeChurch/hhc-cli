package operation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrProcessOutputLimit = errors.New("process_output_limit")
var ErrProcessFailed = errors.New("media_process_failed")

// RunTool executes an absolute, caller-verified bundled binary without a shell.
// It bounds stdout, discards stderr (never echo untrusted media diagnostics),
// and terminates its descendants on cancellation. Bundle verification belongs
// to the caller; this function never searches PATH or downloads a binary.
func RunTool(ctx context.Context, binary string, args []string, outputLimit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(binary) || strings.ContainsRune(binary, 0) || outputLimit < 1 || outputLimit > 1<<20 {
		return nil, os.ErrInvalid
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, os.ErrInvalid
		}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	output := boundedProcessOutput{remaining: outputLimit, cancel: cancel}
	err := runTool(ctx, binary, args, &output)
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if err != nil {
		return nil, ErrProcessFailed
	}
	return output.buffer.Bytes(), nil
}

type boundedProcessOutput struct {
	buffer    bytes.Buffer
	remaining int
	cancel    context.CancelCauseFunc
}

func (w *boundedProcessOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		w.cancel(ErrProcessOutputLimit)
		return 0, ErrProcessOutputLimit
	}
	w.remaining -= len(p)
	return w.buffer.Write(p)
}

var _ io.Writer = (*boundedProcessOutput)(nil)
