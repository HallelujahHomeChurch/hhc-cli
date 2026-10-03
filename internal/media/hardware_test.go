package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestAutoEncoderRequiresRealProbe(t *testing.T) {
	got, err := selectEncoder(context.Background(), "windows", func(string) error {
		t.Fatal("Windows must use NVENC without probing another encoder")
		return operation.ErrProcessFailed
	})
	if err != nil || got != "h264_nvenc" {
		t.Fatalf("selection: %s %v", got, err)
	}
	got, err = selectEncoder(context.Background(), "darwin", func(string) error { return operation.ErrProcessFailed })
	if err != nil || got != "libx264" {
		t.Fatal("unavailable hardware must use CPU")
	}
	_, err = selectEncoder(context.Background(), "darwin", func(string) error { return ErrInsufficientDisk })
	if !errors.Is(err, ErrInsufficientDisk) {
		t.Fatal("disk failure hidden by fallback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = selectEncoder(ctx, "windows", func(string) error { t.Fatal("probed cancelled job"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation")
	}
}

func TestCpuFallbackRequiresIndependentHardwareFailure(t *testing.T) {
	for _, input := range []error{ErrInvalidInput, ErrInsufficientDisk, context.Canceled} {
		fallback, err := hardwareStopped(context.Background(), input, func() error { t.Fatal("non-hardware error reprobed"); return nil })
		if fallback || err != nil {
			t.Fatal("invalid fallback")
		}
	}
	fallback, err := hardwareStopped(context.Background(), operation.ErrProcessFailed, func() error { return nil })
	if fallback || err != nil {
		t.Fatal("healthy hardware retried a broken source")
	}
	fallback, err = hardwareStopped(context.Background(), operation.ErrProcessFailed, func() error { return operation.ErrProcessFailed })
	if !fallback || err != nil {
		t.Fatal("confirmed device failure did not permit fallback")
	}
}

func TestProbeTimeoutFallsBackButOuterCancellationStops(t *testing.T) {
	outer := context.Background()
	probe, cancel := context.WithDeadline(outer, time.Now().Add(-time.Second))
	defer cancel()
	got, err := selectEncoder(outer, "darwin", func(string) error { return qualificationError(outer, probe, context.DeadlineExceeded) })
	if err != nil || got != "libx264" {
		t.Fatal("private probe deadline blocked CPU")
	}
	fallback, err := hardwareStopped(outer, operation.ErrProcessFailed, func() error { return qualificationError(outer, probe, context.DeadlineExceeded) })
	if err != nil || !fallback {
		t.Fatal("device hang blocked bounded fallback")
	}
	canceled, stop := context.WithCancel(outer)
	stop()
	if !errors.Is(qualificationError(canceled, probe, context.DeadlineExceeded), context.Canceled) {
		t.Fatal("outer cancellation hidden")
	}
}

func TestFailedHardwareProbeCannotHideCleanupFailure(t *testing.T) {
	errProbe := errors.Join(operation.ErrProcessFailed, ErrLocalCleanup)
	_, err := selectEncoder(context.Background(), "darwin", func(string) error { return errProbe })
	if !errors.Is(err, ErrLocalCleanup) {
		t.Fatal("selection hid probe cleanup failure")
	}
	fallback, err := hardwareStopped(context.Background(), operation.ErrProcessFailed, func() error { return errProbe })
	if fallback || !errors.Is(err, ErrLocalCleanup) {
		t.Fatal("restart hid probe cleanup failure")
	}
}

func TestNativeHardwareQualification(t *testing.T) {
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" || runtime.GOOS != "darwin" {
		t.Skip("requires native macOS media tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	plan, err := PlanSource(SourceInfo{Width: 1920, Height: 1080, FrameRate: 30, SampleAspectRatio: 1, DurationSeconds: 65, HasAudio: true}, DefaultEncodeOptions())
	if err != nil {
		t.Fatal(err)
	}
	err = qualifyEncoder(ctx, ffmpeg, ffprobe, t.TempDir(), plan.Renditions, "h264_videotoolbox")
	if err != nil {
		if os.Getenv("HHC_REQUIRE_HARDWARE") == "1" {
			t.Fatal(err)
		}
		t.Skipf("hardware not qualified on this host: %v", err)
	}
}

func TestNativeAutoPrepareAlignedPackage(t *testing.T) {
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" || runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("requires supported native media tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	parent := t.TempDir()
	// MKV + FLAC exercises the same non-MOV input path as OBS recordings.
	// MP4/AAC remains covered by the CPU and native CLI preparation fixtures.
	source := filepath.Join(parent, "source.mkv")
	if data, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "35", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "flac", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, data)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	noNVENC := runtime.GOOS == "windows" && os.Getenv("HHC_TEST_NO_NVENC") == "1"
	options := DefaultEncodeOptions()
	options.Progress = func(p EncodingProgress) {
		if noNVENC && p.Encoder != "h264_nvenc" {
			t.Errorf("unexpected fallback: %s", p.Encoder)
		}
	}
	value, err := PrepareAuto(ctx, source, filepath.Join(parent, "output"), ffmpeg, ffprobe, options, nil)
	if noNVENC {
		var failure *PreparationFailure
		if !errors.As(err, &failure) || failure.Stage != "encode_nvenc" || !errors.Is(err, operation.ErrProcessFailed) || value.CPUFallback {
			t.Fatalf("runner without NVIDIA must fail at NVENC, never fallback: %v %+v", err, value.EncodingSummary)
		}
		after, readErr := os.ReadFile(source)
		if readErr != nil || sha256.Sum256(after) != sha256.Sum256(original) {
			t.Fatal("changed source")
		}
		if _, statErr := os.Stat(filepath.Join(parent, "output")); !os.IsNotExist(statErr) {
			t.Fatal("failed encode finalized output")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Inventory.Renditions) != 2 || value.ActualEncoder == "" {
		t.Fatal("incomplete auto package")
	}
	expected := "h264_videotoolbox"
	if runtime.GOOS == "windows" {
		expected = "h264_nvenc"
	}
	if os.Getenv("HHC_REQUIRE_HARDWARE") == "1" && value.ActualEncoder != expected {
		t.Fatalf("expected actual hardware: %s", value.ActualEncoder)
	}
	if _, err := ReadPackage(ctx, value.OutputPath); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual encoder: %s; preset: %s", value.ActualEncoder, value.PresetVersion)
}

func TestHardwareArgumentsKeepFixedMediaContract(t *testing.T) {
	r := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, DurationSeconds: 65, SegmentCount: 3, VideoBitrate: 1500000, AudioBitrate: 128000}
	for _, encoder := range []string{"h264_nvenc", "h264_qsv", "h264_amf", "h264_videotoolbox"} {
		args, err := encodeArguments(filepath.Join(t.TempDir(), "source.mp4"), filepath.Join(t.TempDir(), "720p"), r, encoder)
		if err != nil || !slices.Contains(args, encoder) || slices.Contains(args, "libx264") {
			t.Fatalf("%s: %v %v", encoder, args, err)
		}
		for key, value := range map[string]string{"-b:v": "1500000", "-maxrate": "2000000", "-hls_time": "30", "-pix_fmt": "yuv420p", "-force_key_frames": "expr:gte(t,n_forced*30)"} {
			i := slices.Index(args, key)
			if i < 0 || i+1 >= len(args) || args[i+1] != value {
				t.Fatalf("%s changed %s", encoder, key)
			}
		}
		if slices.Contains(args, "-sc_threshold") || encoder != "h264_nvenc" && slices.Contains(args, "-rc-lookahead") {
			t.Fatal("CPU-only options leaked into hardware path")
		}
	}
	if _, err := encodeArguments(filepath.Join(t.TempDir(), "source.mp4"), filepath.Join(t.TempDir(), "720p"), r, "arbitrary"); err == nil {
		t.Fatal("unapproved encoder")
	}
}
