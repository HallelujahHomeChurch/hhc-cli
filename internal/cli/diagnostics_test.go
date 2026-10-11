package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
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

func TestRecordingRemoteDiagnosticsDistinguishFailures(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		server string
		want   string
	}{
		{"not_found", 404, "", "找不到錄影"},
		{"operation_conflict", 409, "", "操作衝突"},
		{"operation_conflict", 409, "recording_draft_limit", "未完成上傳的錄影已達上限"},
		{"state_changed", 412, "", "錄影版本已變更"},
		{"state_changed", 428, "", "缺少版本前提"},
	} {
		code, message, exit, retry := classify(&api.Error{Code: tc.code, Status: tc.status, Stage: "recording_create", ServerCode: tc.server})
		if code != tc.code || exit != 5 || retry || !strings.Contains(message, tc.want) || !strings.Contains(message, "建立錄影") || !strings.Contains(message, "HTTP ") {
			t.Fatalf("diagnostic: %s %q %d %v", code, message, exit, retry)
		}
	}
}

func TestRecordingDiagnosticsJSONPreservesRecovery(t *testing.T) {
	var out, diagnostic bytes.Buffer
	operationID := "6e06211c-7713-4ca7-b689-1b19dde16959"
	c := commandContext{output: &out, diagnostics: &diagnostic, jsonMode: true, result: result{SchemaVersion: 1, OperationID: &operationID}}
	exit := c.finish(&api.Error{Code: "operation_conflict", Status: 409, Stage: "recording_create", ServerCode: "recording_draft_limit"})
	var got result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if exit != 5 || got.OK || got.SchemaVersion != 1 || got.Error == nil {
		t.Fatalf("unexpected result: %s", out.String())
	}
	e := got.Error
	if e.Code != "operation_conflict" || e.Stage != "recording_create" || e.HTTPStatus != 409 || e.ServerCode != "recording_draft_limit" || e.ResumeOperationID == nil || *e.ResumeOperationID != operationID || e.Retryable || diagnostic.Len() != 0 {
		t.Fatalf("lost diagnostics/recovery: %s", out.String())
	}
}

func TestQuotaDiagnosticDoesNotMislabelAnotherStage(t *testing.T) {
	_, message, _, _ := classify(&api.Error{Code: "operation_conflict", Status: 409, Stage: "package_complete", ServerCode: "recording_draft_limit"})
	if strings.Contains(message, "已達上限") || !strings.Contains(message, "提交上傳完成") {
		t.Fatal(message)
	}
}
