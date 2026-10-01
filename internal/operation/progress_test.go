package operation

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBlockedProgressCannotPreventProcessCancellation(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("native runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer close(release)
	go func() {
		done <- runProgressTool(ctx, os.Args[0], []string{"-test.run=^TestRunToolChild$", "--", "echo", "out_time_us=100\nprogress=continue\n"}, func(MediaProgress) { close(started); <-release })
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reporter not started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked progress prevented cleanup")
	}
}

func TestMediaProgressIsBoundedAndNumericOnly(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var got []MediaProgress
	w := mediaProgressWriter{cancel: cancel, report: func(p MediaProgress) { got = append(got, p) }}
	for _, chunk := range []string{"out_time_us=125", "0000\nspeed=1.5x\nignored=private-path\nprogress=continue\n", "out_time_us=N/A\nspeed=NaN\nprogress=end\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Elapsed != 1250*time.Millisecond || got[0].Speed != 1.5 {
		t.Fatalf("unexpected progress: %+v", got)
	}
	if _, err := w.Write([]byte(strings.Repeat("x", 4097))); !errors.Is(err, ErrProcessOutputLimit) || !errors.Is(context.Cause(ctx), ErrProcessOutputLimit) {
		t.Fatal("unbounded malformed tool output")
	}
}
