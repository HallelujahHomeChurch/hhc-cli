package operation

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

type MediaProgress struct {
	Elapsed time.Duration
	Speed   float64
}

// RunMediaTool retains the same native process-tree cancellation as RunTool.
// Only numeric FFmpeg progress is exposed; stderr and arbitrary keys stay private.
func RunMediaTool(ctx context.Context, binary string, args []string, report func(MediaProgress)) error {
	return runProgressTool(ctx, binary, append([]string{"-progress", "pipe:1", "-stats_period", "2", "-nostats"}, args...), report)
}

func runProgressTool(ctx context.Context, binary string, args []string, report func(MediaProgress)) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	pending, done := make(chan MediaProgress, 1), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-pending:
				if !ok {
					return
				}
				if report != nil {
					report(p)
				}
			}
		}
	}()
	defer func() {
		close(pending)
		// ponytail: an arbitrary writer cannot be interrupted. At most one
		// blocked reporter per encode survives until CLI exit; no per-event
		// goroutines. Persistent embedding should supply a cancellable writer.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-timer.C:
		}
	}()
	w := mediaProgressWriter{cancel: cancel, report: func(p MediaProgress) {
		select {
		case pending <- p:
		default:
		}
	}}
	return runToolOutput(ctx, binary, args, &w)
}

type mediaProgressWriter struct {
	line   []byte
	value  MediaProgress
	valid  bool
	cancel context.CancelCauseFunc
	report func(MediaProgress)
}

func (w *mediaProgressWriter) Write(data []byte) (int, error) {
	for _, b := range data {
		if b != '\n' {
			if len(w.line) >= 4096 {
				w.cancel(ErrProcessOutputLimit)
				return 0, ErrProcessOutputLimit
			}
			w.line = append(w.line, b)
			continue
		}
		key, value, _ := strings.Cut(strings.TrimSuffix(string(w.line), "\r"), "=")
		w.line = w.line[:0]
		switch key {
		case "out_time_us":
			n, err := strconv.ParseInt(value, 10, 64)
			w.valid = err == nil && n >= 0 && n <= int64(24*time.Hour/time.Microsecond)
			if w.valid {
				w.value.Elapsed = time.Duration(n) * time.Microsecond
			}
		case "speed":
			speed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64)
			w.value.Speed = 0
			if err == nil && !math.IsNaN(speed) && !math.IsInf(speed, 0) && speed >= 0 && speed <= 1e6 {
				w.value.Speed = speed
			}
		case "progress":
			if w.valid && (value == "continue" || value == "end") && w.report != nil {
				w.report(w.value)
			}
			w.value, w.valid = MediaProgress{}, false
		}
	}
	return len(data), nil
}
