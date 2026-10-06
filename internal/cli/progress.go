package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
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
	now            func() time.Time
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
	if value.Phase == "local_validation" {
		p.render("", 0, "", fmt.Sprintf("本機檢查 %s · %d/%d 片段 · 已耗%.0f秒", value.Rendition, value.SegmentsVerified, value.SegmentsTotal, value.ElapsedSeconds))
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
	if value.State == "freezing" || value.State == "validating" {
		p.processing(value.ProcessingProgress)
		return
	}
	if !p.interactive {
		return
	}
	switch value.State {
	case "uploading":
		if value.TotalBytes <= 0 {
			return
		}
		p.render("上傳", int(float64(max(0, min(value.CompletedBytes, value.TotalBytes)))/float64(value.TotalBytes)*100), "", "")
	}
}

func (p *progressDisplay) processing(value *api.ProcessingProgress) {
	if !value.Valid() {
		value = nil
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	line := "上傳完成 - 等待影片驗證（進度未知）"
	if value != nil {
		phase := map[string]string{"queued": "等待處理", "source_finalization": "確認來源", "encoding": "轉檔", "package_validation": "驗證影片", "package_finalization": "完成套件"}[value.Phase]
		elapsed := max(0, int(now.Sub(value.AttemptStartedAt).Minutes()))
		idle := max(0, int(now.Sub(value.LastProgressAt).Minutes()))
		line = fmt.Sprintf("%s · 第%d次 · 已耗%d分", phase, value.Attempt, elapsed)
		if value.ObjectsVerified != nil && value.ObjectsTotal != nil {
			line += fmt.Sprintf(" · %d/%d 物件", *value.ObjectsVerified, *value.ObjectsTotal)
		}
		if idle >= 5 {
			line += fmt.Sprintf(" · %d分未有進展", idle)
		}
		line += " · 仍等待 ready"
	}
	if p.interactive {
		p.render("", 0, "", line)
		return
	}
	if p.jsonMode {
		data, _ := json.Marshal(struct {
			Type               string                  `json:"type"`
			OperationID        string                  `json:"operationId"`
			ProcessingProgress *api.ProcessingProgress `json:"processingProgress"`
		}{"processing_progress", p.operationID, value})
		line = string(data)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || line == p.lastLine {
		return
	}
	p.lastLine = line
	fmt.Fprintln(p.writer, line)
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
			if p.Phase == "local_validation" {
				fmt.Fprintf(diagnostics, "本機檢查 %s · %d/%d 片段 · 已耗%.0f秒\n", p.Rendition, p.SegmentsVerified, p.SegmentsTotal, p.ElapsedSeconds)
			} else {
				fmt.Fprintf(diagnostics, "轉檔 %s %.0f%% · %s · %.2fx\n", p.Rendition, p.Fraction*100, p.Encoder, p.Speed)
			}
		}
	}
}
