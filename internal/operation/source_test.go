package operation

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStableSourcePreservesOriginalAndRefusesOverwrite(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native Windows or macOS source stability test")
	}
	source := filepath.Join(t.TempDir(), "聚會 原始.mp4")
	if err := os.WriteFile(source, []byte("original recording"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	f, err := OpenStableSource(context.Background(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if runtime.GOOS == "windows" {
		writer, err := os.OpenFile(source, os.O_WRONLY, 0)
		if err == nil {
			writer.Close()
			t.Fatal("source can be changed while held")
		}
		if err := os.Remove(source); err == nil {
			t.Fatal("source can be deleted while held")
		}
	} else {
		if f.Name() == source {
			t.Fatal("macOS source has no private snapshot")
		}
		if err := os.WriteFile(source, []byte("external change"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "original recording" {
		t.Fatalf("unstable source: %q %v", data, err)
	}
	if runtime.GOOS == "darwin" {
		if _, err := OpenStableSource(context.Background(), source, workspace); err == nil {
			t.Fatal("overwrote existing snapshot")
		}
	}
}

func TestStableSourceRejectsUnsafeInputsAndCancellation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"relative.mp4", "https://example.invalid/source.mp4", filepath.Dir(source)} {
		if f, err := OpenStableSource(context.Background(), input, t.TempDir()); err == nil {
			f.Close()
			t.Fatal("unsafe source accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f, err := OpenStableSource(ctx, source, t.TempDir()); err == nil {
		f.Close()
		t.Fatal("cancelled source accepted")
	}
}
