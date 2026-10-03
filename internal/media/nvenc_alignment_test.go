package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestNVENCHLSPacketsDoNotRequirePresentationReordering(t *testing.T) {
	for _, fps := range []float64{30, 30000.0 / 1001, 24, 20} {
		r := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: fps, DurationSeconds: 1115, SegmentCount: 38, VideoBitrate: 1500000, AudioBitrate: 128000}
		args, err := encodeArguments(filepath.Join(t.TempDir(), "source.mkv"), filepath.Join(t.TempDir(), "720p"), r, "h264_nvenc")
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.Index(args, "-bf"); i < 0 || i+1 >= len(args) || args[i+1] != "0" {
			t.Fatalf("NVENC still requires presentation reordering at %.6f fps", fps)
		}
	}
}

// Exercise the shared fMP4 mux path with NVENC's reorder setting on CPU-only
// runners; require actual NVENC when running the existing hardware acceptance.
func TestNVENCReorderSettingKeepsLongFractionalMKVAligned(t *testing.T) {
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("HHC_REQUIRE_MEDIA_TESTS") == "1" {
			t.Fatal("media tools required")
		}
		t.Skip("media tools unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	source, output := filepath.Join(dir, "fractional.mkv"), filepath.Join(dir, "720p")
	// A legitimate small source offset plus encoder reordering must not
	// push a fragment past the unchanged 100 ms audio/video boundary contract.
	fixture := []string{"-v", "error", "-nostdin", "-itsoffset", "0.045", "-f", "lavfi", "-i", "color=s=160x90:r=30000/1001", "-f", "lavfi", "-i", "sine=sample_rate=48000", "-t", "1115", "-c:v", "libx264", "-preset", "ultrafast", "-bf", "0", "-c:a", "flac", source}
	if out, err := exec.CommandContext(ctx, ffmpeg, fixture...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	info, err := ProbeSource(ctx, ffprobe, source)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanSource(info, DefaultEncodeOptions())
	if err != nil {
		t.Fatal(err)
	}
	r := plan.Renditions[0]
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	nvenc, err := encodeArguments(source, output, r, "h264_nvenc")
	if err != nil {
		t.Fatal(err)
	}
	args, err := CPUEncodeArguments(source, output, r)
	if err != nil {
		t.Fatal(err)
	}
	args[slices.Index(args, "-bf")+1] = nvenc[slices.Index(nvenc, "-bf")+1]
	if runtime.GOOS == "windows" && os.Getenv("HHC_REQUIRE_HARDWARE") == "1" {
		args = nvenc
	}
	if _, err := operation.RunTool(ctx, ffmpeg, args, 1<<20); err != nil {
		t.Fatal(err)
	}
	measured, err := MeasureRendition(ctx, ffprobe, output, r)
	if err != nil {
		t.Fatalf("long fractional MKV rejected with NVENC reorder setting: %v", err)
	}
	if len(measured.SegmentBytes) < 38 {
		t.Fatal("fixture did not reach the reported segment 36")
	}
}
