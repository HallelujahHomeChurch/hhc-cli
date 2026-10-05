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

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestPrepareFailureRetainsCheckpointAndProbeStage(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native stable source")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source.mp4")
	if err := os.WriteFile(source, []byte("read-only source fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint := false
	_, err := PrepareCPU(context.Background(), source, filepath.Join(parent, "output"), filepath.Join(parent, "ffmpeg"), filepath.Join(parent, "missing-probe"), DefaultEncodeOptions(), func(f SourceFingerprint) error {
		checkpoint = f.SHA256 != ""
		return nil
	})
	var failure *PreparationFailure
	if !checkpoint || !errors.As(err, &failure) || failure.Stage != "source_probe" || !errors.Is(err, operation.ErrProcessFailed) {
		t.Fatalf("checkpoint/probe diagnostics: %v %v", checkpoint, err)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "read-only source fixture" {
		t.Fatal("source changed")
	}
}

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
	checkpointErr := errors.New("checkpoint refused")
	checkpointCalled := false
	_, err = PrepareCPU(ctx, source, output, ffmpeg, ffprobe, DefaultEncodeOptions(), func(f SourceFingerprint) error {
		checkpointCalled = true
		if f.SHA256 != fmt.Sprintf("%x", hash) {
			t.Fatal("checkpoint used another source")
		}
		return checkpointErr
	})
	if !checkpointCalled || !errors.Is(err, checkpointErr) {
		t.Fatalf("checkpoint not enforced: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("output created before source checkpoint")
	}
	options := DefaultEncodeOptions()
	progress := make(map[string]float64)
	options.Progress = func(p EncodingProgress) { progress[p.Rendition] = p.Fraction }
	value, err := PrepareCPU(ctx, source, output, ffmpeg, ffprobe, options, nil)
	if err != nil || len(value.Inventory.Renditions) != 3 || value.SourceFingerprint.SHA256 != fmt.Sprintf("%x", hash) || value.ActualEncoder != "libx264" {
		t.Fatalf("prepare: %+v %v", value, err)
	}
	if progress["720p+1080p+480p"] < .99 {
		t.Fatalf("did not stream shared encoding progress: %+v", progress)
	}
	verified, err := ReadPackage(ctx, output)
	if err != nil || verified.InventoryDigest != value.Inventory.InventoryDigest {
		t.Fatalf("output not valid: %v", err)
	}
	after, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("changed original")
	}
	if _, err := PrepareCPU(ctx, source, output, ffmpeg, ffprobe, DefaultEncodeOptions(), nil); err == nil {
		t.Fatal("overwrote user output")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := PrepareCPU(cancelled, source, filepath.Join(parent, "cancelled"), ffmpeg, ffprobe, DefaultEncodeOptions(), nil); !errors.Is(err, context.Canceled) {
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
