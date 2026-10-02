package operation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

var ErrProcessOutputLimit = errors.New("process_output_limit")
var ErrProcessFailed = errors.New("media_process_failed")

// Failure metadata is deliberately limited to a fixed stage and numeric exit
// code. Neither source paths nor arbitrary media-tool stderr are returned.
type ProcessFailure struct {
	Stage      string
	ExitCode   int
	SystemCode uint64
}

func (e *ProcessFailure) Error() string {
	return fmt.Sprintf("media_process_failed (%s exit=%d os=%d)", e.Stage, e.ExitCode, e.SystemCode)
}
func (e *ProcessFailure) Unwrap() error { return ErrProcessFailed }

func processFailure(stage string, err error) *ProcessFailure {
	var code syscall.Errno
	errors.As(err, &code)
	return &ProcessFailure{Stage: stage, ExitCode: -1, SystemCode: uint64(code)}
}

// RunTool executes an absolute, caller-verified bundled binary without a shell.
// It bounds stdout, discards stderr (never echo untrusted media diagnostics),
// and terminates its descendants on cancellation. Bundle verification belongs
// to the caller; this function never searches PATH or downloads a binary.
func RunTool(ctx context.Context, binary string, args []string, outputLimit int) ([]byte, error) {
	if outputLimit < 1 || outputLimit > 1<<20 {
		return nil, os.ErrInvalid
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	output := boundedProcessOutput{remaining: outputLimit, cancel: cancel}
	if err := runToolOutput(ctx, binary, args, &output); err != nil {
		return nil, err
	}
	return output.buffer.Bytes(), nil
}

func runToolOutput(ctx context.Context, binary string, args []string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(binary) || !utf8.ValidString(binary) || strings.ContainsRune(binary, 0) {
		return os.ErrInvalid
	}
	for _, arg := range args {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return os.ErrInvalid
		}
	}
	err := runTool(ctx, binary, args, output)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err != nil {
		var failure *ProcessFailure
		if errors.As(err, &failure) {
			return failure
		}
		code := -1
		var exited interface{ ExitCode() int }
		if errors.As(err, &exited) {
			code = exited.ExitCode()
		}
		return &ProcessFailure{Stage: "tool", ExitCode: code}
	}
	return nil
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
