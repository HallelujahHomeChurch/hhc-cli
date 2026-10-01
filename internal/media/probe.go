package media

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

// ProbeSource must receive the stable handle's path, not an arbitrary URL.
// The caller verifies ffprobe against the release bundle before invoking it.
func ProbeSource(ctx context.Context, ffprobe, source string) (SourceInfo, error) {
	if err := ctx.Err(); err != nil {
		return SourceInfo{}, err
	}
	if !filepath.IsAbs(source) {
		return SourceInfo{}, ErrInvalidInput
	}
	switch strings.ToLower(filepath.Ext(source)) {
	case ".mp4", ".mkv", ".mov":
	default:
		return SourceInfo{}, ErrUnsupportedSource
	}
	before, err := os.Lstat(source)
	if err != nil {
		return SourceInfo{}, err
	}
	if !before.Mode().IsRegular() {
		return SourceInfo{}, ErrUnsupportedSource
	}
	if err := ValidateRecordingSourceSize(before.Size()); err != nil {
		return SourceInfo{}, err
	}
	data, err := operation.RunTool(ctx, ffprobe, []string{
		"-v", "error", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska", "-enable_drefs", "0",
		"-show_streams", "-show_format", "-of", "json", source,
	}, 1<<20)
	if err != nil {
		return SourceInfo{}, err
	}
	after, err := os.Lstat(source)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return SourceInfo{}, operation.ErrSourceChanged
	}
	return ParseSourceProbe(bytes.NewReader(data))
}

// ParseSourceProbe accepts bounded local ffprobe metadata, not proof that a
// source is stable or safe to open. The process runner must restrict protocols
// and demuxers and acquire the platform's stable source/snapshot first.
func ParseSourceProbe(reader io.Reader) (SourceInfo, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return SourceInfo{}, ErrUnsupportedSource
	}
	var probe struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width         int    `json:"width"`
			Height        int    `json:"height"`
			SAR           string `json:"sample_aspect_ratio"`
			FrameRate     string `json:"avg_frame_rate"`
			ColorTransfer string `json:"color_transfer"`
			Disposition   struct {
				AttachedPicture int `json:"attached_pic"`
			} `json:"disposition"`
			SideData []struct {
				Rotation int `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Name     string `json:"format_name"`
			Duration string `json:"duration"`
		} `json:"format"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&probe) != nil || len(probe.Streams) > 64 {
		return SourceInfo{}, ErrUnsupportedSource
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return SourceInfo{}, ErrUnsupportedSource
	}
	container := strings.Split(probe.Format.Name, ",")[0]
	if container != "mov" && container != "matroska" {
		return SourceInfo{}, ErrUnsupportedSource
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		return SourceInfo{}, ErrUnsupportedSource
	}
	source := SourceInfo{DurationSeconds: duration}
	video := false
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" {
			source.HasAudio = true
		}
		if stream.CodecType != "video" || stream.Disposition.AttachedPicture != 0 || video {
			continue
		}
		video = true
		source.Width, source.Height, source.ColorTransfer = stream.Width, stream.Height, stream.ColorTransfer
		source.SampleAspectRatio = rational(stream.SAR, ":")
		source.FrameRate = rational(stream.FrameRate, "/")
		for _, side := range stream.SideData {
			if side.Rotation != 0 {
				source.Rotation = side.Rotation
			}
		}
	}
	if !video || !validSourceInfo(source) {
		return SourceInfo{}, ErrUnsupportedSource
	}
	return source, nil
}

func rational(value, separator string) float64 {
	parts := strings.Split(value, separator)
	if len(parts) != 2 {
		return 0
	}
	n, e1 := strconv.ParseFloat(parts[0], 64)
	d, e2 := strconv.ParseFloat(parts[1], 64)
	if e1 != nil || e2 != nil || !finitePositive(n) || !finitePositive(d) {
		return 0
	}
	return n / d
}
