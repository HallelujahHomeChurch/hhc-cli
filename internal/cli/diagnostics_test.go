package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestNVENCFailureIsActionableWithoutChangingRecoveryCode(t *testing.T) {
	code, message, exit, retry := classify(&media.PreparationFailure{
		Stage: "encode_nvenc", Cause: &operation.ProcessFailure{Stage: "tool", ExitCode: 1},
	})
	if code != "media_process_failed" || exit != 1 || retry || !strings.Contains(message, "NVIDIA") || !strings.Contains(message, "不會") || !strings.Contains(message, "resume") {
		t.Fatalf("NVENC failure: %s %q %d %v", code, message, exit, retry)
	}
	code, message, _, _ = classify(&media.PreparationFailure{Stage: "encode_nvenc", Cause: context.DeadlineExceeded})
	if code != "timeout" || strings.Contains(message, "驅動") {
		t.Fatalf("timeout misdiagnosed as driver failure: %s %s", code, message)
	}
}

func TestHLSValidationReportsSafeCheckWithoutChangingRecovery(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "720p")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.m3u8"), []byte("https://secret.invalid/private-token"), 0600); err != nil {
		t.Fatal(err)
	}
	_, cause := media.MeasureRendition(context.Background(), "unused", directory, media.RecordingRendition{
		Name: "720p", Width: 1280, Height: 720, FrameRate: 30, DurationSeconds: 30,
		SegmentCount: 1, VideoBitrate: 1500000, AudioBitrate: 128000,
	})
	code, message, exit, retry := classify(&media.PreparationFailure{Stage: "encode_nvenc", Cause: cause})
	if code != "invalid_input" || exit != 2 || retry || !strings.Contains(message, "hls_validate") || !strings.Contains(message, "720p") || !strings.Contains(message, "playlist_closure") {
		t.Fatalf("missing HLS diagnostic: %s %q %d %v", code, message, exit, retry)
	}
	if strings.Contains(message, directory) || strings.Contains(message, "private-token") || strings.Contains(message, "驅動") {
		t.Fatalf("unsafe or misleading diagnostic: %s", message)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:30\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:30,\nseg-000000.m4s\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(directory, "index.m3u8"), []byte(playlist), 0600); err != nil {
		t.Fatal(err)
	}
	_, cause = media.MeasureRendition(context.Background(), "unused", directory, media.RecordingRendition{
		Name: "720p", Width: 1280, Height: 720, FrameRate: 30, DurationSeconds: 30,
		SegmentCount: 1, VideoBitrate: 1500000, AudioBitrate: 128000,
	})
	code, message, _, _ = classify(&media.PreparationFailure{Stage: "encode_nvenc", Cause: cause})
	if !errors.Is(cause, media.ErrInvalidInput) || code != "invalid_input" || !strings.Contains(message, "segment=0 check=fragment_read") || strings.Contains(message, directory) {
		t.Fatalf("missing safe fragment diagnostic: %s %s", code, message)
	}
}
