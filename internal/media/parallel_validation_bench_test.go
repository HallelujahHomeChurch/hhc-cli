package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Compare the same real package; encoding is outside the timed region.
func BenchmarkRenditionValidation(b *testing.B) {
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" || (runtime.GOOS != "darwin" && runtime.GOOS != "windows") {
		b.Skip("explicit native media fixture tools required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := b.TempDir()
	source := filepath.Join(root, "source.mp4")
	output := filepath.Join(root, "package")
	if data, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "65", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", source).CombinedOutput(); err != nil {
		b.Fatalf("fixture: %v %s", err, data)
	}
	prepared, err := PrepareCPU(ctx, source, output, ffmpeg, ffprobe, DefaultEncodeOptions(), nil)
	if err != nil {
		b.Fatal(err)
	}
	validate := func(ctx context.Context, r RecordingRendition) (RenditionMedia, error) {
		return MeasureRendition(ctx, ffprobe, filepath.Join(output, r.Name), r)
	}
	for _, parallel := range []bool{false, true} {
		name := "serial"
		if parallel {
			name = "parallel"
		}
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if parallel {
					if _, err := validateRenditions(ctx, prepared.Inventory.Renditions, validate); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, r := range prepared.Inventory.Renditions {
						if _, err := validate(ctx, r); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
