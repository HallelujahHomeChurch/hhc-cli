package mcp

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"io"
	"testing"
	"time"
)

func TestNullArgumentsRejected(t *testing.T) {
	r, _ := Decode([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_videos","arguments":null}}`))
	called := false
	out := Handle(context.Background(), r, func(context.Context, string, videos.Input) (videos.Result, error) {
		called = true
		return videos.Result{}, nil
	}, "test")
	if called || out.Error == nil || out.Error.Code != -32602 {
		t.Fatal("null arguments accepted")
	}
}
func TestIdleStdioCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeStdio(ctx, reader, io.Discard, nil, "test") }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle stdio read did not cancel")
	}
}
