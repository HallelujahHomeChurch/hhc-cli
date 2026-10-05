package media

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMultiEncodeSharesInputAndKeepsQualityForEveryOutput(t *testing.T) {
	plan, err := PlanSource(SourceInfo{Width: 1920, Height: 1080, FrameRate: 30, SampleAspectRatio: 1, DurationSeconds: 65, HasAudio: true}, DefaultEncodeOptions())
	if err != nil {
		t.Fatal(err)
	}
	source, dir := filepath.Join(t.TempDir(), "原始.mkv"), t.TempDir()
	args, err := multiEncodeArguments(source, dir, plan.Renditions, "h264_nvenc")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-enable_drefs") {
		t.Fatal("MOV-only restriction on MKV")
	}
	counts := map[string]int{}
	for _, a := range args {
		counts[a]++
	}
	if counts["-i"] != 1 || counts["h264_nvenc"] != 3 || counts["p6"] != 3 || counts["expr:gte(t,n_forced*30)"] != 3 {
		t.Fatalf("wrong source/output options: %v", args)
	}
	for _, name := range []string{"480p", "720p", "1080p"} {
		if !slices.Contains(args, filepath.ToSlash(filepath.Join(dir, name, "index.m3u8"))) {
			t.Fatal("missing output", name)
		}
	}
	if _, err := multiEncodeArguments(source, dir, append(plan.Renditions, plan.Renditions[0]), "h264_nvenc"); err == nil {
		t.Fatal("duplicate output accepted")
	}
}

func TestMasterAcceptsThreeAlignedRenditionsInAnyInventoryOrder(t *testing.T) {
	var media []RenditionMedia
	for _, r := range []RecordingRendition{
		{Name: "1080p", Width: 1920, Height: 1080, VideoBitrate: 3000000},
		{Name: "480p", Width: 854, Height: 480, VideoBitrate: 800000},
		{Name: "720p", Width: 1280, Height: 720, VideoBitrate: 1500000},
	} {
		r.FrameRate = 30
		r.AudioBitrate = 128000
		r.DurationSeconds = 5
		r.SegmentCount = 1
		media = append(media, RenditionMedia{Rendition: r, SegmentBytes: []int64{100}, SegmentDurations: []float64{5}, TargetDuration: 5, Codecs: "avc1.640028,mp4a.40.2"})
	}
	data, err := BuildMasterPlaylist(media)
	if err != nil || strings.Count(string(data), "#EXT-X-STREAM-INF:") != 3 {
		t.Fatalf("three renditions rejected: %s %v", data, err)
	}
	media[1].StartSeconds = .5
	if _, err := BuildMasterPlaylist(media); err == nil {
		t.Fatal("unaligned rendition accepted")
	}
}
