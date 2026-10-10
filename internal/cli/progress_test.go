package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
)

func TestValidationProgressDisplay(t *testing.T) {
	now := time.Now().UTC()
	zero, total := int64(0), int64(100)
	value := recordings.TransferProgress{State: "validating", ProcessingProgress: &api.ProcessingProgress{Attempt: 2, Phase: "package_validation", ObjectsVerified: &zero, ObjectsTotal: &total, AttemptStartedAt: now.Add(-10 * time.Minute), PhaseStartedAt: now.Add(-8 * time.Minute), LastProgressAt: now.Add(-6 * time.Minute), HeartbeatAt: now}}
	var output bytes.Buffer
	p := &progressDisplay{writer: &output, interactive: true, width: func() int { return 160 }, now: func() time.Time { return now }}
	p.Transfer(value)
	if !strings.Contains(output.String(), "0/100") || !strings.Contains(output.String(), "第2次") || !strings.Contains(output.String(), "6分未有進展") || !strings.Contains(output.String(), "仍等待 ready") {
		t.Fatalf("lost safe progress: %q", output.String())
	}
	if strings.Contains(output.String(), "100%") || strings.Contains(output.String(), "\n") {
		t.Fatal("progress manufactured completion or extra lines")
	}
	output.Reset()
	p = newProgressDisplay(&output, true, "operation", false)
	p.Transfer(value)
	var event struct {
		Type               string                  `json:"type"`
		ProcessingProgress *api.ProcessingProgress `json:"processingProgress"`
	}
	if json.Unmarshal(output.Bytes(), &event) != nil || event.Type != "processing_progress" || event.ProcessingProgress == nil || *event.ProcessingProgress.ObjectsVerified != 0 || strings.Contains(output.String(), "\r") {
		t.Fatalf("invalid numeric JSON event: %q", output.String())
	}
}

func TestValidationJSONRetainsHeartbeatUpdates(t *testing.T) {
	now := time.Now().UTC()
	value := &api.ProcessingProgress{Attempt: 1, Phase: "package_validation", AttemptStartedAt: now.Add(-time.Minute), PhaseStartedAt: now.Add(-time.Minute), LastProgressAt: now.Add(-time.Minute), HeartbeatAt: now}
	var output bytes.Buffer
	p := newProgressDisplay(&output, true, "operation", false)
	p.now = func() time.Time { return now }
	p.Transfer(recordings.TransferProgress{State: "validating", ProcessingProgress: value})
	value.HeartbeatAt = now.Add(time.Second)
	p.Transfer(recordings.TransferProgress{State: "validating", ProcessingProgress: value})
	if strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("heartbeat update lost: %s", output.String())
	}
}

func TestTerminalProgressRewritesOneLineAndEndsBeforeError(t *testing.T) {
	var output bytes.Buffer
	p := &progressDisplay{writer: &output, interactive: true, width: func() int { return 80 }}
	p.Encoding(media.EncodingProgress{Rendition: "720p", Encoder: "h264_nvenc", Fraction: .5, Speed: 3.58})
	p.Encoding(media.EncodingProgress{Rendition: "1080p", Encoder: "h264_nvenc", Fraction: .25, Speed: 2})
	p.Transfer(recordings.TransferProgress{CompletedBytes: 75, TotalBytes: 100, State: "uploading"})
	p.Transfer(recordings.TransferProgress{CompletedBytes: 100, TotalBytes: 100, State: "validating"})
	if strings.Contains(output.String(), "\n") || !strings.Contains(output.String(), "50%") || !strings.Contains(output.String(), "75%") || !strings.Contains(output.String(), "[#") || !strings.Contains(output.String(), "等待影片驗證") {
		t.Fatalf("not a single-line phase-aware progress bar: %q", output.String())
	}
	p.Close()
	p.Close()
	p.Encoding(media.EncodingProgress{Rendition: "720p", Encoder: "h264_nvenc", Fraction: .9, Speed: 3.58})
	fmt.Fprintln(&output, "original diagnostic")
	if strings.Count(output.String(), "\n") != 2 || !strings.HasSuffix(output.String(), "\noriginal diagnostic\n") {
		t.Fatalf("error overwritten or extra close newline: %q", output.String())
	}
}

func TestProgressFitsNarrowAndResizedTerminal(t *testing.T) {
	for _, width := range []int{80, 40, 20, 8, 1} {
		var output bytes.Buffer
		columns := 80
		p := &progressDisplay{writer: &output, interactive: true, width: func() int { return columns }}
		p.Encoding(media.EncodingProgress{Rendition: "720p", Encoder: "h264_nvenc", Fraction: .1, Speed: 3.58})
		columns = width
		p.Encoding(media.EncodingProgress{Rendition: "720p", Encoder: "h264_nvenc", Fraction: .5, Speed: math.Inf(1)})
		frames := strings.Split(output.String(), "\r")
		last := frames[len(frames)-1]
		// Only fixed Chinese labels and ASCII details are rendered.
		cells := 0
		for _, r := range last {
			cells++
			if r > 127 {
				cells++
			}
		}
		if cells >= width || strings.Contains(last, "\n") || strings.Contains(last, "\x1b") {
			t.Fatalf("progress wraps terminal width %d: %q (%d cells)", width, last, cells)
		}
	}
}

func TestProgressPreservesNonterminalAndJSONOutput(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		var output bytes.Buffer
		p := newProgressDisplay(&output, jsonMode, "operation", true)
		p.Encoding(media.EncodingProgress{Rendition: "720p", Encoder: "h264_nvenc", Fraction: .5, Speed: 3.58})
		p.Transfer(recordings.TransferProgress{CompletedBytes: 1, TotalBytes: 2, State: "uploading"})
		p.Close()
		if strings.Contains(output.String(), "\r") || strings.Count(output.String(), "\n") != 1 {
			t.Fatalf("rewrote redirected/JSON output: %q", output.String())
		}
		if jsonMode {
			var value struct {
				Type, OperationID string
				Fraction          float64
			}
			if json.Unmarshal(output.Bytes(), &value) != nil || value.Type != "encoding_progress" || value.OperationID != "operation" || value.Fraction != .5 {
				t.Fatalf("changed JSON encoding contract: %q", output.String())
			}
		} else if output.String() != "轉檔 720p 50% · h264_nvenc · 3.58x\n" {
			t.Fatalf("changed line-based encoding output: %q", output.String())
		}
	}
}

func TestParallelValidationProgressRetainsAllRenditions(t *testing.T) {
	var output bytes.Buffer
	p := newProgressDisplay(&output, false, "operation", false)
	p.interactive = true
	p.width = func() int { return 100 }
	for _, name := range []string{"1080p", "480p", "720p"} {
		p.Encoding(media.EncodingProgress{Phase: "local_validation", Rendition: name, SegmentsVerified: 2, SegmentsTotal: 9})
	}
	for _, expected := range []string{"480p 2/9", "720p 2/9", "1080p 2/9"} {
		if !strings.Contains(p.lastLine, expected) {
			t.Fatalf("missing %s: %s", expected, p.lastLine)
		}
	}
	p.Close()
}
