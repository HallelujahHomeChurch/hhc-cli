package media

import (
	"strings"
	"testing"
)

func TestMasterUsesMeasuredSegmentBandwidthInsteadOfEncodeSettings(t *testing.T) {
	low := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 35, SegmentCount: 2}
	high := low
	high.Name = "1080p"
	high.Width = 1920
	high.Height = 1080
	high.VideoBitrate = 3000000
	media := []RenditionMedia{
		{Rendition: high, SegmentBytes: []int64{200, 400}, SegmentDurations: []float64{30, 5}, TargetDuration: 30, Codecs: "avc1.640028,mp4a.40.2"},
		{Rendition: low, SegmentBytes: []int64{100, 100}, SegmentDurations: []float64{30, 5}, TargetDuration: 30, Codecs: "avc1.64001f,mp4a.40.2"},
	}
	master, err := BuildMasterPlaylist(media)
	if err != nil {
		t.Fatal(err)
	}
	want := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-STREAM-INF:BANDWIDTH=46,AVERAGE-BANDWIDTH=46,RESOLUTION=1280x720,FRAME-RATE=30.000,CODECS=\"avc1.64001f,mp4a.40.2\"\n720p/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=138,AVERAGE-BANDWIDTH=138,RESOLUTION=1920x1080,FRAME-RATE=30.000,CODECS=\"avc1.640028,mp4a.40.2\"\n1080p/index.m3u8\n"
	if string(master) != want {
		t.Fatalf("incorrect measured master: %s", master)
	}
	if strings.Contains(string(master), "1500000") || strings.Contains(string(master), "3000000") {
		t.Fatal("used configured bitrate")
	}
	for _, mutate := range []func(*RenditionMedia){
		func(m *RenditionMedia) { m.Codecs = "hev1.1.6.L93,mp4a.40.2" }, func(m *RenditionMedia) { m.Rendition.Name = "../escape" },
		func(m *RenditionMedia) { m.SegmentBytes = []int64{100} }, func(m *RenditionMedia) { m.SegmentDurations = []float64{30, 4} },
		func(m *RenditionMedia) { m.TargetDuration = 0 }, func(m *RenditionMedia) { m.SegmentBytes = []int64{0, 100} },
	} {
		bad := append([]RenditionMedia(nil), media...)
		mutate(&bad[0])
		if _, err := BuildMasterPlaylist(bad); err == nil {
			t.Fatal("accepted invalid media")
		}
	}
	if _, err := BuildMasterPlaylist(media[:1]); err == nil {
		t.Fatal("accepted high-only package")
	}
}

func TestMeasuredBandwidthMatchesOwnerGoldenWindows(t *testing.T) {
	for _, tc := range []struct {
		sizes         []int64
		durations     []float64
		target        int
		peak, average int64
	}{
		{[]int64{100, 100}, []float64{30, 5}, 30, 46, 46},
		{[]int64{3750000, 7500000, 625000}, []float64{30, 30, 5}, 30, 2000000, 1461539},
		{[]int64{7}, []float64{5}, 5, 12, 12},
	} {
		peak, average, err := RecordingPlaylistBitrates(tc.sizes, tc.durations, tc.target)
		if err != nil || peak != tc.peak || average != tc.average {
			t.Fatalf("rates %d/%d %v", peak, average, err)
		}
	}
}
