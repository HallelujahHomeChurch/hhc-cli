package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPrepareCPUPreservesSourceAndAtomicallyCreatesPackage(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native stable-source contract requires Windows/macOS")
	}
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("explicit native media fixture tools required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	parent := t.TempDir()
	source := filepath.Join(parent, "原始聚會.mp4")
	output := filepath.Join(parent, "影音套件")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r=2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "35", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(original)
	value, err := PrepareCPU(ctx, source, output, ffmpeg, ffprobe, DefaultEncodeOptions())
	if err != nil || len(value.Inventory.Renditions) != 2 || value.SourceFingerprint.SHA256 != fmt.Sprintf("%x", hash) || value.ActualEncoder != "libx264" {
		t.Fatalf("prepare: %+v %v", value, err)
	}
	verified, err := ReadPackage(ctx, output)
	if err != nil || verified.InventoryDigest != value.Inventory.InventoryDigest {
		t.Fatalf("output not valid: %v", err)
	}
	after, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("changed original")
	}
	if _, err := PrepareCPU(ctx, source, output, ffmpeg, ffprobe, DefaultEncodeOptions()); err == nil {
		t.Fatal("overwrote user output")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := PrepareCPU(cancelled, source, filepath.Join(parent, "cancelled"), ffmpeg, ffprobe, DefaultEncodeOptions()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prepare: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hhc-") {
			t.Fatalf("left source snapshot or scratch: %s", entry.Name())
		}
	}
}

func TestStagingBudgetStopsOversizedGeneratedBytes(t *testing.T) {
	directory := t.TempDir()
	f, err := os.Create(filepath.Join(directory, "oversized.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(RecordingPackageMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if err := checkStagingBudget(directory); !errors.Is(err, ErrRecordingPackageTooLarge) {
		t.Fatalf("unchecked staging budget: %v", err)
	}
}
