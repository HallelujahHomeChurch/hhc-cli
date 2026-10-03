package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
)

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
