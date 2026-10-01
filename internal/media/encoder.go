package media

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// CPUEncodeArguments builds the fixed libx264-medium fallback. The caller must
// hold a stable source and provide a new, private, empty rendition directory.
// Executing these arguments alone is not a complete validated HLS package.
func CPUEncodeArguments(source, output string, rendition RecordingRendition) ([]string, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(output) || strings.ContainsRune(source+output, 0) || !validRecordingRendition(rendition) || filepath.Base(output) != rendition.Name {
		return nil, ErrInvalidInput
	}
	i := func(value int64) string { return strconv.FormatInt(value, 10) }
	fps := strconv.FormatFloat(rendition.FrameRate, 'f', -1, 64)
	gop := strconv.Itoa(int(math.Ceil(rendition.FrameRate * 30)))
	maxrate := rendition.VideoBitrate * 4 / 3
	// x264 medium's 40-frame lookahead can exceed mux interleave buffering
	// for low-frame-rate sources and push audio into the wrong HLS fragment.
	// Keep the normal 40 frames, capped to two seconds for sparse video.
	lookahead := strconv.Itoa(min(40, max(1, int(math.Ceil(rendition.FrameRate*2)))))
	bframes := "3"
	if rendition.FrameRate < 20 {
		bframes = "0"
	} // Keep DTS/PTS segment boundaries within 100 ms for sparse video.
	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-n",
		"-protocol_whitelist", "file", "-format_whitelist", "mov,matroska", "-enable_drefs", "0",
		"-noautorotate", "-threads", "2", "-i", filepath.ToSlash(source),
		"-map", "0:V:0", "-map", "0:a:0", "-map_metadata", "-1", "-map_chapters", "-1",
		"-filter_threads", "2", "-vf", "scale=" + strconv.Itoa(rendition.Width) + ":" + strconv.Itoa(rendition.Height) + ":flags=lanczos,setsar=1",
		"-c:v", "libx264", "-preset", "medium", "-profile:v", "high", "-pix_fmt", "yuv420p", "-threads", "2",
		"-rc-lookahead", lookahead, "-bf", bframes,
		"-b:v", i(rendition.VideoBitrate), "-maxrate", i(maxrate), "-bufsize", i(2 * maxrate),
		"-r", fps, "-fps_mode", "cfr", "-g", gop, "-keyint_min", gop, "-sc_threshold", "0", "-flags", "+cgop",
		"-force_key_frames", "expr:gte(t,n_forced*30)",
		"-c:a", "aac", "-b:a", "128000", "-ar", "48000", "-ac", "2",
		"-f", "hls", "-hls_time", "30", "-hls_playlist_type", "vod", "-hls_segment_type", "fmp4",
		"-hls_flags", "independent_segments", "-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.ToSlash(filepath.Join(output, "seg-%06d.m4s")), filepath.ToSlash(filepath.Join(output, "index.m3u8")),
	}, nil
}

func validRecordingRendition(r RecordingRendition) bool {
	maxWidth, maxHeight := 1280, 720
	if r.Name == "1080p" {
		maxWidth, maxHeight = 1920, 1080
	} else if r.Name != "720p" {
		return false
	}
	return r.Width > 0 && r.Width <= maxWidth && r.Width%2 == 0 && r.Height > 0 && r.Height <= maxHeight && r.Height%2 == 0 &&
		finitePositive(r.FrameRate) && r.FrameRate <= 30 && finitePositive(r.DurationSeconds) && r.DurationSeconds <= RecordingMaxDurationSeconds &&
		r.SegmentCount == int(math.Ceil(r.DurationSeconds/30)) && r.AudioBitrate == 128000 && r.VideoBitrate >= 500000 && r.VideoBitrate <= 8000000
}
