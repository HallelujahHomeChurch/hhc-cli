package media

import (
	"math"
	"testing"
)

func TestPreparePlanPreservesSourceAspectAndDoesNotUpscale(t *testing.T) {
	for _, test := range []struct {
		width, height              int
		fps                        float64
		count, lowWidth, lowHeight int
	}{
		{1920, 1080, 60, 2, 1280, 720}, {640, 360, 24, 1, 640, 360}, {1080, 1920, 30, 2, 404, 720},
	} {
		plan, err := PlanSource(SourceInfo{Width: test.width, Height: test.height, SampleAspectRatio: 1, FrameRate: test.fps, DurationSeconds: 65, HasAudio: true}, DefaultEncodeOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Renditions) != test.count || plan.Renditions[0].Width != test.lowWidth || plan.Renditions[0].Height != test.lowHeight {
			t.Fatalf("wrong dimensions: %+v", plan)
		}
		for _, r := range plan.Renditions {
			if r.FrameRate != math.Min(30, test.fps) || r.Width > test.width || r.Height > test.height || r.SegmentCount != 3 || r.AudioBitrate != 128_000 {
				t.Fatalf("upscale/timeline mismatch: %+v", r)
			}
		}
	}
}

func TestPreparePlanRejectsUnsupportedSourceAndBudget(t *testing.T) {
	base := SourceInfo{Width: 1920, Height: 1080, SampleAspectRatio: 1, FrameRate: 30, DurationSeconds: 65, HasAudio: true}
	for _, mutate := range []func(*SourceInfo){
		func(s *SourceInfo) { s.HasAudio = false }, func(s *SourceInfo) { s.Rotation = 90 },
		func(s *SourceInfo) { s.ColorTransfer = "smpte2084" }, func(s *SourceInfo) { s.ColorTransfer = "arib-std-b67" },
		func(s *SourceInfo) { s.FrameRate = math.NaN() }, func(s *SourceInfo) { s.SampleAspectRatio = 0 },
		func(s *SourceInfo) { s.DurationSeconds = 18000 },
	} {
		source := base
		mutate(&source)
		if _, err := PlanSource(source, DefaultEncodeOptions()); err == nil {
			t.Fatalf("accepted unsupported/budget source: %+v", source)
		}
	}
	options := DefaultEncodeOptions()
	options.VideoBitrate1080 = 500_000
	if _, err := PlanSource(base, options); err == nil {
		t.Fatal("accepted high rendition below low bitrate")
	}
	near := base
	near.DurationSeconds = 15000
	plan, err := PlanSource(near, DefaultEncodeOptions())
	if err != nil || !plan.NearCapacity {
		t.Fatalf("missing 9GB capacity warning: %+v %v", plan, err)
	}
}
