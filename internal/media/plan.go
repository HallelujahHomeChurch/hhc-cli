package media

import (
	"errors"
	"math"
)

var ErrUnsupportedSource = errors.New("unsupported_source")

type SourceInfo struct {
	Width, Height                                 int
	SampleAspectRatio, FrameRate, DurationSeconds float64
	HasAudio                                      bool
	Rotation                                      int
	ColorTransfer                                 string
}

type EncodeOptions struct {
	VideoBitrate720, VideoBitrate1080 int64
	SegmentSeconds                    int
	Encoder                           string
	Progress                          func(EncodingProgress)
}

type EncodingProgress struct {
	Rendition string  `json:"rendition"`
	Encoder   string  `json:"encoder"`
	Fraction  float64 `json:"fraction"`
	Speed     float64 `json:"speed"`
}

func DefaultEncodeOptions() EncodeOptions {
	return EncodeOptions{VideoBitrate720: 1_500_000, VideoBitrate1080: 3_000_000, SegmentSeconds: 30, Encoder: "auto"}
}

type EncodePlan struct {
	Renditions     []RecordingRendition
	EstimatedBytes int64
	NearCapacity   bool
}

// PlanSource consumes bounded probe metadata, not user-supplied upload claims.
// No source is silently tone-mapped, rotated, upscaled or truncated.
func PlanSource(source SourceInfo, options EncodeOptions) (EncodePlan, error) {
	if !validSourceInfo(source) {
		return EncodePlan{}, ErrUnsupportedSource
	}
	if options.SegmentSeconds != 30 || options.Encoder != "auto" || options.VideoBitrate720 < 500_000 || options.VideoBitrate720 > 8_000_000 || options.VideoBitrate1080 < options.VideoBitrate720 || options.VideoBitrate1080 > 8_000_000 {
		return EncodePlan{}, ErrInvalidInput
	}
	width := float64(source.Width) * source.SampleAspectRatio
	height := float64(source.Height)
	plan := EncodePlan{Renditions: make([]RecordingRendition, 0, 3)}
	for _, limit := range []struct {
		name          string
		width, height float64
		bitrate       int64
	}{{"720p", 1280, 720, options.VideoBitrate720}, {"1080p", 1920, 1080, options.VideoBitrate1080}, {"480p", 854, 480, min(800000, options.VideoBitrate720)}} {
		scale := math.Min(1, math.Min(limit.width/width, limit.height/height))
		w, h := int(math.Floor(width*scale/2))*2, int(math.Floor(height*scale/2))*2
		if w < 2 || h < 2 {
			return EncodePlan{}, ErrUnsupportedSource
		}
		if len(plan.Renditions) > 0 && (limit.name == "1080p" && h <= plan.Renditions[0].Height || limit.name == "480p" && h >= plan.Renditions[0].Height) {
			continue
		}
		plan.Renditions = append(plan.Renditions, RecordingRendition{Name: limit.name, Width: w, Height: h, FrameRate: math.Min(30, source.FrameRate), VideoBitrate: limit.bitrate, AudioBitrate: 128_000, DurationSeconds: source.DurationSeconds, SegmentCount: int(math.Ceil(source.DurationSeconds / 30))})
	}
	bitrates := make([]int64, len(plan.Renditions))
	for i, r := range plan.Renditions {
		bitrates[i] = r.VideoBitrate
	}
	size, err := EstimateRecordingPackageSize(source.DurationSeconds, bitrates)
	if err != nil {
		return EncodePlan{}, err
	}
	plan.EstimatedBytes, plan.NearCapacity = size, size >= 9_000_000_000
	return plan, nil
}

func validSourceInfo(source SourceInfo) bool {
	if source.Width <= 0 || source.Height <= 0 || source.Width > 8192 || source.Height > 8192 || !finitePositive(source.SampleAspectRatio) || source.SampleAspectRatio > 16 || !finitePositive(source.FrameRate) || !finitePositive(source.DurationSeconds) || source.DurationSeconds > RecordingMaxDurationSeconds || !source.HasAudio || source.Rotation != 0 {
		return false
	}
	switch source.ColorTransfer {
	case "", "unknown", "unspecified", "bt709", "smpte170m", "smpte240m", "gamma22", "gamma28", "iec61966-2-1":
		return true
	}
	return false
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
