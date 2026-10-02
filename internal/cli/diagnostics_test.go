package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func TestSafeNativeFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err          error
		code, detail string
	}{
		{&media.PreparationFailure{Stage: "source_probe", Cause: &operation.ProcessFailure{Stage: "create_process", ExitCode: -1, SystemCode: 5}}, "media_process_failed", "階段 source_probe：媒體工具失敗（stage=create_process exit=-1 os=5）"},
		{&media.PreparationFailure{Stage: "source_checkpoint", Cause: &os.PathError{Op: "open", Path: "sensitive-fixture", Err: syscall.Errno(5)}}, "preparation_failed", "os=5"},
		{&media.PreparationFailure{Stage: "encode_renditions", Cause: context.DeadlineExceeded}, "timeout", "階段 encode_renditions"},
		{&media.PreparationFailure{Stage: "encoder_qualification", Cause: media.ErrLocalCleanup}, "local_cleanup_failed", "階段 encoder_qualification"},
		{errors.Join(&media.PreparationFailure{Stage: "source_probe", Cause: &operation.ProcessFailure{Stage: "tool", ExitCode: 7}}, nil), "media_process_failed", "階段 source_probe"},
		{errors.Join(&media.PreparationFailure{Stage: "source_probe", Cause: &operation.ProcessFailure{Stage: "tool", ExitCode: 7}}, media.ErrLocalCleanup), "local_cleanup_failed", "階段 source_probe"},
		{errors.Join(&operation.ProcessFailure{Stage: "tool", ExitCode: 7}, media.ErrLocalCleanup), "local_cleanup_failed", "暫存清理"},
		{&update.CleanupFailure{SystemCode: 32}, "update_cleanup_failed", "data.installed"},
	} {
		code, message, exit, _ := classify(tc.err)
		if code != tc.code || exit == 0 || !strings.Contains(message, tc.detail) || strings.Contains(message, "sensitive-fixture") {
			t.Fatalf("classification: %s %q %d", code, message, exit)
		}
	}
}
