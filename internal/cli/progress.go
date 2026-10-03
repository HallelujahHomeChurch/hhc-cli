package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
	"golang.org/x/term"
)

type progressDisplay struct {
	writer         io.Writer
	jsonMode       bool
	operationID    string
	interactive    bool
	width          func() int
	mu             sync.Mutex
	active, closed bool
	lastLine       string
	lastWidth      int
}

func newProgressDisplay(writer io.Writer, jsonMode bool, operationID string, interactive bool) *progressDisplay {
	p := &progressDisplay{writer: writer, jsonMode: jsonMode, operationID: operationID}
	if file, ok := writer.(*os.File); ok && interactive && !jsonMode && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(file.Fd())) {
		p.interactive = true
		p.width = func() int {
			width, _, err := term.GetSize(int(file.Fd()))
			if err != nil || width <= 0 {
				return 80
			}
			return width
		}
	}
	return p
}

func (p *progressDisplay) Encoding(value media.EncodingProgress) {
	if !p.interactive {
		recordingProgress(p.writer, p.jsonMode, p.operationID)(value)
		return
	}
	fraction := value.Fraction
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		fraction = 0
	}
	speed := value.Speed
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0 {
		speed = 0
	}
	p.render("轉檔 "+value.Rendition, int(max(0, min(1, fraction))*100), fmt.Sprintf(" | %s | %.2fx", value.Encoder, speed), "")
}

func (p *progressDisplay) Transfer(value recordings.TransferProgress) {
	// Keep existing JSON and redirected logs unchanged; this observer is UI only.
	if !p.interactive {
		return
	}
	switch value.State {
	case "uploading":
		if value.TotalBytes <= 0 {
			return
		}
		p.render("上傳", int(float64(max(0, min(value.CompletedBytes, value.TotalBytes)))/float64(value.TotalBytes)*100), "", "")
	case "freezing", "validating":
		p.render("", 0, "", "上傳完成 - 等待影片驗證")
	}
}

func (p *progressDisplay) render(label string, percent int, detail, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	limit := max(0, p.width()-1) // Reserve the final column to avoid auto-wrap.
	line := status
	if status == "" {
		counter := fmt.Sprintf(" %3d%%", percent)
		barWidth := min(20, limit-displayCells(label+counter)-3-displayCells(detail))
		if barWidth < 4 {
			detail = ""
			barWidth = min(20, limit-displayCells(label+counter)-3)
		}
		line = label + counter
		if barWidth >= 4 {
			filled := barWidth * percent / 100
			line = label + " [" + strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled) + "]" + counter + detail
		} else if displayCells(line) > limit {
			line = fmt.Sprintf("%d%%", percent)
		}
	}
	line = fitProgressLine(line, limit)
	if line == p.lastLine && p.active {
		return
	}
	cells := displayCells(line)
	padding := min(max(0, p.lastWidth-cells), max(0, limit-cells))
	// Carriage return and spaces work on Windows consoles without enabling ANSI.
	fmt.Fprintf(p.writer, "\r%s%s", line, strings.Repeat(" ", padding))
	p.lastLine, p.lastWidth, p.active = line, cells, true
}

func (p *progressDisplay) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active {
		fmt.Fprintln(p.writer)
	}
	p.active, p.closed = false, true
}

// ponytail: fixed Han/ASCII labels only; use grapheme-aware widths if arbitrary
// Unicode labels or emoji are introduced, not a general terminal-width engine.
func displayCells(value string) int {
	cells := 0
	for _, r := range value {
		cells++
		if unicode.Is(unicode.Han, r) {
			cells++
		}
	}
	return cells
}

func fitProgressLine(value string, limit int) string {
	cells := 0
	for offset, r := range value {
		width := 1
		if unicode.Is(unicode.Han, r) {
			width++
		}
		if cells+width > limit {
			return value[:offset]
		}
		cells += width
	}
	return value
}

func recordingProgress(diagnostics io.Writer, jsonMode bool, operationID string) func(media.EncodingProgress) {
	return func(p media.EncodingProgress) {
		if jsonMode {
			_ = json.NewEncoder(diagnostics).Encode(struct {
				Type        string `json:"type"`
				OperationID string `json:"operationId"`
				media.EncodingProgress
			}{"encoding_progress", operationID, p})
		} else {
			fmt.Fprintf(diagnostics, "轉檔 %s %.0f%% · %s · %.2fx\n", p.Rendition, p.Fraction*100, p.Encoder, p.Speed)
		}
	}
}
