package media

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

type RenditionMedia struct {
	Rendition        RecordingRendition
	SegmentBytes     []int64
	SegmentDurations []float64
	TargetDuration   int
	Codecs           string // Must come from the actual encoded AVCC/AAC probe.
	StartSeconds     float64
}

var recordingCodecs = regexp.MustCompile(`^avc1\.[0-9a-fA-F]{6},mp4a\.40\.2$`)

func BuildMasterPlaylist(media []RenditionMedia) ([]byte, error) {
	if len(media) < 1 || len(media) > 3 {
		return nil, ErrInvalidInput
	}
	media = slices.Clone(media)
	slices.SortFunc(media, func(a, b RenditionMedia) int { return a.Rendition.Height - b.Rendition.Height })
	low := media[0].Rendition
	if !slices.ContainsFunc(media, func(m RenditionMedia) bool { return m.Rendition.Name == "720p" }) {
		return nil, ErrInvalidInput
	}
	var master strings.Builder
	master.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for n, m := range media {
		r := m.Rendition
		if !validRecordingRendition(r) || !recordingCodecs.MatchString(m.Codecs) || len(m.SegmentBytes) != r.SegmentCount || math.IsNaN(m.StartSeconds) || math.IsInf(m.StartSeconds, 0) {
			return nil, ErrInvalidInput
		}
		if n > 0 && (r.Name == "480p" || media[n-1].Rendition.Name == "1080p" || r.Height <= media[n-1].Rendition.Height || r.VideoBitrate < media[n-1].Rendition.VideoBitrate || r.FrameRate != low.FrameRate || math.Abs(r.DurationSeconds-low.DurationSeconds) > 1/low.FrameRate+0.001 || math.Abs(m.StartSeconds-media[0].StartSeconds) > 1/low.FrameRate+0.001) {
			return nil, ErrInvalidInput
		}
		peak, average, err := RecordingPlaylistBitrates(m.SegmentBytes, m.SegmentDurations, m.TargetDuration)
		if err != nil {
			return nil, err
		}
		duration := 0.0
		for _, d := range m.SegmentDurations {
			duration += d
		}
		if math.Abs(duration-r.DurationSeconds) > 1/r.FrameRate+0.001 {
			return nil, ErrInvalidInput
		}
		fmt.Fprintf(&master, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d,FRAME-RATE=%.3f,CODECS=\"%s\"\n%s/index.m3u8\n", peak, average, r.Width, r.Height, r.FrameRate, m.Codecs, r.Name)
	}
	return []byte(master.String()), nil
}

// Pinned to Asset producer 5e5cdfa / RFC 8216 section 4.1. This intentionally
// shares its golden windows, not an encoder's configured target bitrate.
func RecordingPlaylistBitrates(sizes []int64, durations []float64, target int) (int64, int64, error) {
	if len(sizes) == 0 || len(sizes) != len(durations) || len(sizes) > RecordingPackageMaxObjects || target < 1 || target > 31 {
		return 0, 0, ErrInvalidInput
	}
	var totalSize int64
	totalDuration := 0.0
	for n, size := range sizes {
		d := durations[n]
		if size <= 0 || size > RecordingObjectMaxBytes || !finitePositive(d) || d > 31 || (n < len(sizes)-1 && d < 29) {
			return 0, 0, ErrInvalidInput
		}
		totalSize += size
		totalDuration += d
	}
	if totalSize > RecordingPackageMaxBytes || totalDuration > RecordingMaxDurationSeconds {
		return 0, 0, ErrInvalidInput
	}
	peak := 0.0
	for start := range sizes {
		var bytes int64
		seconds := 0.0
		// HHC's >=29s non-tail segments bound each eligible window to two.
		for end := start; end < len(sizes); end++ {
			bytes += sizes[end]
			seconds += durations[end]
			if seconds > 1.5*float64(target) {
				break
			}
			if seconds >= 0.5*float64(target) {
				peak = math.Max(peak, float64(bytes)*8/seconds)
			}
		}
	}
	if peak == 0 {
		return 0, 0, ErrInvalidInput
	}
	return int64(math.Ceil(peak)), int64(math.Ceil(float64(totalSize) * 8 / totalDuration)), nil
}
