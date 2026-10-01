package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestCPUEncodeRejectsNetworkInputAndInvalidRendition(t *testing.T) {
	r := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 65, SegmentCount: 3}
	for _, source := range []string{"https://example.invalid/source.mp4", "relative.mp4", ""} {
		if _, err := CPUEncodeArguments(source, filepath.Join(t.TempDir(), "720p"), r); err == nil {
			t.Fatal("accepted nonlocal source")
		}
	}
	for _, mutate := range []func(*RecordingRendition){
		func(r *RecordingRendition) { r.Width++ }, func(r *RecordingRendition) { r.FrameRate = math.NaN() },
		func(r *RecordingRendition) { r.Name = "../escape" }, func(r *RecordingRendition) { r.VideoBitrate = 0 },
		func(r *RecordingRendition) { r.AudioBitrate = 64000 }, func(r *RecordingRendition) { r.FrameRate = 60 },
	} {
		bad := r
		mutate(&bad)
		if _, err := CPUEncodeArguments(filepath.Join(t.TempDir(), "source.mp4"), t.TempDir(), bad); err == nil {
			t.Fatalf("accepted bad rendition: %+v", bad)
		}
	}
}

// Explicit tools are a test fixture only; production must verify its bundle.
func TestCPUEncodeProducesAlignedThirtySecondVOD(t *testing.T) {
	for _, fps := range []int{2, 30} {
		t.Run(strconv.Itoa(fps), func(t *testing.T) { testCPUEncodeAligned(t, fps) })
	}
}

func testCPUEncodeAligned(t *testing.T, fps int) {
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("HHC_REQUIRE_MEDIA_TESTS") == "1" {
			t.Fatal("media test tools required")
		}
		t.Skip("set absolute HHC_TEST_FFMPEG and HHC_TEST_FFPROBE for actual media checks")
	}
	if !filepath.IsAbs(ffmpeg) || !filepath.IsAbs(ffprobe) {
		t.Fatal("absolute test tools required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(tool string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, tool, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("media fixture: %v: %s", err, out)
		}
		return out
	}
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "聚會 HLS")
	if err := os.Mkdir(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "聚會 原始.mp4")
	run(ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=blue:s=1920x1080:r="+strconv.Itoa(fps), "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "65", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", source)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(before)
	sourceFile, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, fingerprintErr := FingerprintSource(ctx, sourceFile)
	closeErr := sourceFile.Close()
	if fingerprintErr != nil || closeErr != nil || fingerprint.SHA256 != fmt.Sprintf("%x", hash) || fingerprint.SizeBytes != int64(len(before)) {
		t.Fatalf("actual source fingerprint: %+v %v %v", fingerprint, fingerprintErr, closeErr)
	}
	metadata, err := ProbeSource(ctx, ffprobe, source)
	if err != nil || metadata.Width != 1920 || metadata.Height != 1080 || metadata.FrameRate != float64(fps) || metadata.DurationSeconds != 65 || !metadata.HasAudio {
		t.Fatalf("actual source probe: %+v %v", metadata, err)
	}
	plan, err := PlanSource(metadata, DefaultEncodeOptions())
	if err != nil {
		t.Fatal(err)
	}
	var starts []float64
	var measured []RenditionMedia
	for _, r := range plan.Renditions {
		output := filepath.Join(packageDir, r.Name)
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		args, err := CPUEncodeArguments(source, output, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := operation.RunTool(ctx, ffmpeg, args, 1<<20); err != nil {
			t.Fatalf("bounded native encode: %v", err)
		}
		actual, err := MeasureRendition(ctx, ffprobe, output, r)
		if err != nil || len(actual.SegmentBytes) != 3 || actual.SegmentDurations[0] != 30 || actual.Codecs == "" {
			t.Fatalf("bounded encoded measurement: %+v %v", actual, err)
		}
		playlistPath := filepath.Join(output, "index.m3u8")
		playlist, err := os.ReadFile(playlistPath)
		if err != nil {
			t.Fatal(err)
		}
		var durations []float64
		for line := range strings.SplitSeq(string(playlist), "\n") {
			if strings.HasPrefix(line, "#EXTINF:") {
				d, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ","), 64)
				if err != nil {
					t.Fatal(err)
				}
				durations = append(durations, d)
			}
		}
		if len(durations) != 3 || durations[0] != 30 || durations[1] != 30 || durations[2] != 5 || !strings.Contains(string(playlist), "#EXT-X-ENDLIST") || !strings.Contains(string(playlist), `URI="init.mp4"`) {
			t.Fatalf("bad VOD: %s", playlist)
		}
		var probe struct {
			Packets []struct {
				Time  string `json:"pts_time"`
				Flags string `json:"flags"`
			} `json:"packets"`
		}
		if err := json.Unmarshal(run(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_packets", "-show_entries", "packet=pts_time,flags", "-of", "json", playlistPath), &probe); err != nil {
			t.Fatal(err)
		}
		var keys []float64
		for _, packet := range probe.Packets {
			if strings.Contains(packet.Flags, "K") {
				pts, err := strconv.ParseFloat(packet.Time, 64)
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, pts)
			}
		}
		if len(keys) != 3 || math.Abs(keys[1]-keys[0]-30) > 0.001 || math.Abs(keys[2]-keys[0]-60) > 0.001 {
			t.Fatalf("unaligned keyframes %v", keys)
		}
		starts = append(starts, keys[0])
		var media struct {
			Streams []struct {
				Type       string `json:"codec_type"`
				Codec      string `json:"codec_name"`
				Profile    string `json:"profile"`
				Width      int    `json:"width"`
				Height     int    `json:"height"`
				SAR        string `json:"sample_aspect_ratio"`
				Pixels     string `json:"pix_fmt"`
				Rate       string `json:"r_frame_rate"`
				SampleRate string `json:"sample_rate"`
				Channels   int    `json:"channels"`
			} `json:"streams"`
		}
		// Probe init plus a media fragment, as the Asset owner does. ffprobe's
		// HLS demuxer can omit the AAC profile even when the MP4 reports it.
		init, err := os.ReadFile(filepath.Join(output, "init.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		// Fixture-only extraction from FFmpeg's actual AVCC header. Production
		// preparation must use the bounded encoded-media validation path.
		avcc := bytes.Index(init, []byte("avcC"))
		if avcc < 0 || avcc+8 > len(init) || init[avcc+4] != 1 {
			t.Fatal("missing actual AVC configuration")
		}
		var sizes []int64
		for n := range durations {
			info, err := os.Stat(filepath.Join(output, fmt.Sprintf("seg-%06d.m4s", n)))
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, info.Size())
		}
		measured = append(measured, RenditionMedia{Rendition: r, SegmentBytes: sizes, SegmentDurations: durations, TargetDuration: 30, Codecs: fmt.Sprintf("avc1.%02x%02x%02x,mp4a.40.2", init[avcc+5], init[avcc+6], init[avcc+7])})
		fragment, err := os.ReadFile(filepath.Join(output, "seg-000000.m4s"))
		if err != nil {
			t.Fatal(err)
		}
		probePath := filepath.Join(dir, r.Name+"-probe.mp4")
		if err := os.WriteFile(probePath, append(init, fragment...), 0600); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(run(ffprobe, "-v", "error", "-show_streams", "-of", "json", probePath), &media); err != nil {
			t.Fatal(err)
		}
		if len(media.Streams) != 2 {
			t.Fatalf("unexpected streams %+v", media)
		}
		for _, s := range media.Streams {
			switch s.Type {
			case "video":
				if s.Codec != "h264" || s.Width != r.Width || s.Height != r.Height || s.SAR != "1:1" || s.Pixels != "yuv420p" || s.Rate != strconv.Itoa(fps)+"/1" {
					t.Fatalf("bad video %+v", s)
				}
			case "audio":
				if s.Codec != "aac" || s.Profile != "LC" || s.SampleRate != "48000" || s.Channels != 2 {
					t.Fatalf("bad audio %+v", s)
				}
			default:
				t.Fatalf("unexpected stream %+v", s)
			}
		}
		run(ffmpeg, "-v", "error", "-nostdin", "-i", playlistPath, "-f", "null", "-")
	}
	if len(starts) != 2 || math.Abs(starts[0]-starts[1]) > 0.001 {
		t.Fatalf("renditions start differently %v", starts)
	}
	master, err := BuildMasterPlaylist(measured)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "master.m3u8"), master, 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := BuildPackageInventory(ctx, packageDir, plan.Renditions, "cpu-hq-v1")
	if err != nil || len(inv.Objects) != 11 || inv.InventoryDigest == "" {
		t.Fatalf("actual HLS closure: %+v %v", inv, err)
	}
	after, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("source changed")
	}
}
