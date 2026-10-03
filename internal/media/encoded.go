package media

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

// ValidationFailure contains only internally generated checks and numbers.
// Its cause remains inspectable without exposing paths or tool/source output.
type ValidationFailure struct {
	rendition string
	segment   int
	check     string
	cause     error
}

func (e *ValidationFailure) Error() string {
	return fmt.Sprintf("rendition=%s segment=%d check=%s", e.rendition, e.segment, e.check)
}

func (e *ValidationFailure) Unwrap() error { return e.cause }

func invalidEncoded(check string) error {
	return &ValidationFailure{segment: -1, check: check, cause: ErrInvalidInput}
}

// MeasureRendition reads only the fixed files produced in a private rendition
// directory. Each probe sees one init+fragment, never a remote playlist or the
// entire recording. Remote validation remains the authority for ready.
func MeasureRendition(ctx context.Context, ffprobe, directory string, r RecordingRendition) (value RenditionMedia, err error) {
	value = RenditionMedia{Rendition: r}
	check, segment := "rendition_arguments", -1
	defer func() {
		if err == nil {
			return
		}
		var inner *ValidationFailure
		if errors.As(err, &inner) {
			check = inner.check
		}
		name := "unknown"
		if r.Name == "720p" || r.Name == "1080p" {
			name = r.Name
		}
		err = &ValidationFailure{rendition: name, segment: segment, check: check, cause: err}
	}()
	if !filepath.IsAbs(directory) || !validRecordingRendition(r) || filepath.Base(directory) != r.Name {
		return value, ErrInvalidInput
	}
	check = "rendition_directory"
	root, err := os.OpenRoot(directory)
	if err != nil {
		return value, err
	}
	defer root.Close()
	check = "playlist_read"
	playlist, err := readEncodedFile(root, "index.m3u8", RecordingPlaylistMaxBytes)
	if err != nil {
		return value, err
	}
	check = "playlist_parse"
	value.SegmentDurations, value.TargetDuration, err = parseEncodedPlaylist(playlist, r.SegmentCount)
	if err != nil {
		return value, err
	}
	check = "probe_workspace"
	scratch, err := os.MkdirTemp(filepath.Dir(directory), ".measure-")
	if err != nil {
		return value, err
	}
	probePath := filepath.Join(scratch, "segment.mp4")
	defer func() { os.Remove(probePath); os.Remove(scratch) }()
	var start, end float64
	for n, duration := range value.SegmentDurations {
		segment = n
		check = "fragment_read"
		if err := ctx.Err(); err != nil {
			return value, err
		}
		probe, err := os.OpenFile(probePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return value, err
		}
		var size int64
		for _, path := range []string{"init.mp4", fmt.Sprintf("seg-%06d.m4s", n)} {
			info, statErr := root.Lstat(path)
			if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > RecordingObjectMaxBytes {
				probe.Close()
				return value, fmt.Errorf("%w: fragment file %s (unavailable=%t)", ErrInvalidInput, path, statErr != nil)
			}
			file, openErr := root.Open(path)
			if openErr != nil {
				probe.Close()
				return value, openErr
			}
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, opened) {
				file.Close()
				probe.Close()
				return value, ErrInvalidInput
			}
			copied, copyErr := io.Copy(probe, io.LimitReader(file, info.Size()+1))
			file.Close()
			if copyErr != nil || copied != info.Size() {
				probe.Close()
				return value, ErrInvalidInput
			}
			if path != "init.mp4" {
				size = copied
			}
		}
		if err := probe.Close(); err != nil {
			return value, err
		}
		check = "fragment_probe_tool"
		data, err := operation.RunTool(ctx, ffprobe, []string{"-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov", "-enable_drefs", "0", "-use_absolute_path", "0", "-show_data", "-show_streams", "-show_packets", "-show_entries", "stream=index,codec_type,codec_name,width,height,pix_fmt,sample_rate,channels,profile,r_frame_rate,sample_aspect_ratio,extradata:packet=stream_index,pts_time,duration_time,flags", "-of", "json", probePath}, 1<<20)
		if err != nil {
			return value, err
		}
		check = "fragment_probe_parse"
		actual, err := parseEncodedProbe(data, r)
		if err != nil {
			return value, err
		}
		if n == 0 {
			start = actual.start
			value.StartSeconds = start
			value.Codecs = actual.codecs
		} else if math.Abs(actual.start-end) > 1/r.FrameRate+0.001 || actual.codecs != value.Codecs {
			check = fmt.Sprintf("fragment_continuity previous_end=%.6f start=%.6f codecs_match=%t", end, actual.start, actual.codecs == value.Codecs)
			return value, ErrInvalidInput
		}
		if math.Abs(actual.end-actual.start-duration) > 1/r.FrameRate+0.001 {
			check = fmt.Sprintf("fragment_duration actual=%.6f expected=%.6f", actual.end-actual.start, duration)
			return value, ErrInvalidInput
		}
		end = actual.end
		value.SegmentBytes = append(value.SegmentBytes, size)
	}
	segment = -1
	if math.Abs(end-start-r.DurationSeconds) > 1/r.FrameRate+0.001 {
		check = fmt.Sprintf("rendition_duration actual=%.6f expected=%.6f", end-start, r.DurationSeconds)
		return value, ErrInvalidInput
	}
	check = "playlist_bitrate"
	if _, _, err := RecordingPlaylistBitrates(value.SegmentBytes, value.SegmentDurations, value.TargetDuration); err != nil {
		return value, err
	}
	return value, nil
}

func readEncodedFile(root *os.Root, path string, limit int64) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, ErrInvalidInput
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, ErrInvalidInput
	}
	return data, nil
}

func parseEncodedPlaylist(data []byte, count int) ([]float64, int, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 5 || lines[0] != "#EXTM3U" || lines[len(lines)-1] != "#EXT-X-ENDLIST" {
		return nil, 0, invalidEncoded("playlist_closure")
	}
	var durations []float64
	var pending float64
	target := 0
	hasMap := false
	tags := map[string]bool{}
	for _, line := range lines[1 : len(lines)-1] {
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			if pending != 0 || !strings.HasSuffix(line, ",") {
				return nil, 0, invalidEncoded("playlist_duration_tag")
			}
			d, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ","), 64)
			if err != nil || !finitePositive(d) || d > 31 {
				return nil, 0, invalidEncoded("playlist_segment_duration")
			}
			pending = d
		case line == fmt.Sprintf("seg-%06d.m4s", len(durations)):
			if pending == 0 {
				return nil, 0, invalidEncoded("playlist_segment_reference")
			}
			durations = append(durations, pending)
			pending = 0
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if target != 0 {
				return nil, 0, invalidEncoded("playlist_target_duplicate")
			}
			var err error
			target, err = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil || target < 1 || target > 31 {
				return nil, 0, invalidEncoded("playlist_target_duration")
			}
		case line == `#EXT-X-MAP:URI="init.mp4"`:
			if hasMap {
				return nil, 0, invalidEncoded("playlist_map_duplicate")
			}
			hasMap = true
		case line == "#EXT-X-VERSION:7" || line == "#EXT-X-MEDIA-SEQUENCE:0" || line == "#EXT-X-PLAYLIST-TYPE:VOD" || line == "#EXT-X-INDEPENDENT-SEGMENTS":
			if tags[line] {
				return nil, 0, invalidEncoded("playlist_tag_duplicate")
			}
			tags[line] = true
		default:
			return nil, 0, invalidEncoded(fmt.Sprintf("playlist_tag_or_reference segment=%d", len(durations)))
		}
	}
	if !hasMap || pending != 0 || target == 0 || len(durations) != count || !tags["#EXT-X-PLAYLIST-TYPE:VOD"] {
		return nil, 0, invalidEncoded(fmt.Sprintf("playlist_structure segments=%d expected=%d map=%t pending=%t target=%d vod=%t", len(durations), count, hasMap, pending != 0, target, tags["#EXT-X-PLAYLIST-TYPE:VOD"]))
	}
	return durations, target, nil
}

type encodedProbe struct {
	start, end float64
	codecs     string
}

// This wire validation follows the Asset owner's fragment contract, without
// importing the private Asset service into the public CLI distribution.
func parseEncodedProbe(data []byte, r RecordingRendition) (encodedProbe, error) {
	var output struct {
		Streams []struct {
			Index      int    `json:"index"`
			Codec      string `json:"codec_name"`
			Type       string `json:"codec_type"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			Pixels     string `json:"pix_fmt"`
			Aspect     string `json:"sample_aspect_ratio"`
			Rate       string `json:"r_frame_rate"`
			Extra      string `json:"extradata"`
			Profile    string `json:"profile"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Packets []struct {
			Stream   int    `json:"stream_index"`
			PTS      string `json:"pts_time"`
			Duration string `json:"duration_time"`
			Flags    string `json:"flags"`
		} `json:"packets"`
	}
	stage := "streams"
	invalid := func() (encodedProbe, error) {
		return encodedProbe{}, invalidEncoded(stage)
	}
	if len(data) > 1<<20 || json.Unmarshal(data, &output) != nil || len(output.Streams) != 2 || len(output.Packets) == 0 || len(output.Packets) > 4096 {
		return invalid()
	}
	video, audio := -1, -1
	codecs := ""
	for _, s := range output.Streams {
		switch s.Type {
		case "video":
			stage = "video_stream"
			a, b, ok := strings.Cut(s.Rate, "/")
			num, e1 := strconv.ParseFloat(a, 64)
			den, e2 := strconv.ParseFloat(b, 64)
			if video != -1 || s.Index < 0 || s.Codec != "h264" || s.Width != r.Width || s.Height != r.Height || s.Pixels != "yuv420p" || s.Aspect != "1:1" || !ok || e1 != nil || e2 != nil || !finitePositive(num) || !finitePositive(den) || math.Abs(num/den-r.FrameRate) > 0.001 {
				return invalid()
			}
			stage = "video_codec_configuration"
			first := strings.Split(strings.TrimSpace(s.Extra), "\n")[0]
			fields := strings.Fields(first)
			if len(fields) < 4 || fields[0] != "00000000:" || len(fields[3]) < 2 {
				return invalid()
			}
			avcc, err := hex.DecodeString(fields[1] + fields[2] + fields[3][:2])
			if err != nil || len(avcc) != 5 || avcc[0] != 1 || avcc[4] != 255 {
				return invalid()
			}
			codecs = "avc1." + hex.EncodeToString(avcc[1:4]) + ",mp4a.40.2"
			video = s.Index
		case "audio":
			stage = "audio_stream"
			if audio != -1 || s.Index < 0 || s.Codec != "aac" || s.Profile != "LC" || s.SampleRate != "48000" || s.Channels < 1 || s.Channels > 2 {
				return invalid()
			}
			audio = s.Index
		default:
			stage = "unexpected_stream"
			return invalid()
		}
	}
	if video < 0 || audio < 0 || video == audio {
		stage = "stream_indices"
		return invalid()
	}
	type packet struct {
		pts, duration float64
		key           bool
	}
	tracks := map[int][]packet{video: nil, audio: nil}
	for n, p := range output.Packets {
		pts, e1 := strconv.ParseFloat(p.PTS, 64)
		duration, e2 := strconv.ParseFloat(p.Duration, 64)
		missing := p.Stream == audio && p.Duration == ""
		if missing {
			duration = 0
			e2 = nil
		}
		if _, ok := tracks[p.Stream]; !ok || e1 != nil || e2 != nil || math.IsNaN(pts) || math.IsInf(pts, 0) || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 && !missing || duration > 1 {
			stage = fmt.Sprintf("packet_fields packet=%d track=%d pts_valid=%t duration_valid=%t duration_missing=%t", n, p.Stream, e1 == nil && !math.IsNaN(pts) && !math.IsInf(pts, 0), e2 == nil && finitePositive(duration) && duration <= 1, p.Duration == "")
			return invalid()
		}
		tracks[p.Stream] = append(tracks[p.Stream], packet{pts, duration, strings.Contains(p.Flags, "K")})
	}
	for stream, packets := range tracks {
		stage = "packet count/keyframe"
		if len(packets) == 0 || stream == video && (len(packets) > 1000 || !packets[0].key) || stream == audio && len(packets) > 2000 {
			stage = fmt.Sprintf("packet count/keyframe track %d video %d count %d", stream, video, len(packets))
			if len(packets) > 0 {
				stage += fmt.Sprintf(" key %t", packets[0].key)
			}
			return invalid()
		}
		slices.SortFunc(packets, func(a, b packet) int {
			if a.pts < b.pts {
				return -1
			}
			if a.pts > b.pts {
				return 1
			}
			return 0
		})
		for i, p := range packets {
			stage = "AAC duration"
			if p.duration == 0 {
				if stream != audio || i != 0 || len(packets) < 2 {
					return invalid()
				}
				p.duration = packets[1].pts - p.pts
				if math.Abs(p.duration-1024.0/48000) > 0.000002 && math.Abs(p.duration-960.0/48000) > 0.000002 {
					return invalid()
				}
				packets[i].duration = p.duration
			}
			if stream == video && math.Abs(p.duration-1/r.FrameRate) > 0.0002 {
				stage = fmt.Sprintf("video duration %.6f expected %.6f", p.duration, 1/r.FrameRate)
				return invalid()
			}
			if i > 0 && (p.pts <= packets[i-1].pts || math.Abs(p.pts-packets[i-1].pts-packets[i-1].duration) > 0.0002) {
				stage = fmt.Sprintf("PTS continuity track %d at %d previous %.6f duration %.6f current %.6f", stream, i, packets[i-1].pts, packets[i-1].duration, p.pts)
				return invalid()
			}
		}
	}
	v, a := tracks[video], tracks[audio]
	stage = "audio/video alignment"
	end := v[len(v)-1].pts + v[len(v)-1].duration
	if math.Abs(v[0].pts-a[0].pts) > 0.1 || math.Abs(end-a[len(a)-1].pts-a[len(a)-1].duration) > 0.1 || end-v[0].pts > 30+1/r.FrameRate+0.001 {
		stage = fmt.Sprintf("audio/video alignment video %.6f..%.6f audio %.6f..%.6f", v[0].pts, end, a[0].pts, a[len(a)-1].pts+a[len(a)-1].duration)
		return invalid()
	}
	return encodedProbe{v[0].pts, end, codecs}, nil
}
