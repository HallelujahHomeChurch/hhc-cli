package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func selectEncoder(ctx context.Context, platform string, probe func(string) error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Windows is NVIDIA-only. The real encode reports device/driver failures;
	// do not probe another device or silently select software encoding.
	if platform == "windows" {
		return "h264_nvenc", nil
	}
	var candidates []string
	switch platform {
	case "darwin":
		candidates = []string{"h264_videotoolbox"}
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		err := probe(candidate)
		if errors.Is(err, ErrLocalCleanup) {
			return "", err
		}
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, operation.ErrProcessFailed) && !errors.Is(err, ErrInvalidInput) {
			return "", err
		}
	}
	return "libx264", ctx.Err()
}

// A failed source encode alone is not proof of hardware failure. Recheck the
// same device with trusted synthetic input before allowing one CPU restart.
func hardwareStopped(ctx context.Context, encodeErr error, probe func() error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !errors.Is(encodeErr, operation.ErrProcessFailed) {
		return false, nil
	}
	err := probe()
	if errors.Is(err, ErrLocalCleanup) {
		return false, err
	}
	if err == nil {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, operation.ErrProcessFailed) {
		return true, nil
	}
	return false, err
}

// Probe the actual target settings, not just the advertised encoder list.
// A tiny synthetic source isolates driver support from user-source corruption.
func qualifyEncoder(ctx context.Context, ffmpeg, ffprobe, parent string, renditions []RecordingRendition, encoder string) (err error) {
	outer := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	defer func() { err = qualificationError(outer, ctx, err) }()
	directory, err := os.MkdirTemp(parent, ".encoder-probe-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			err = errors.Join(err, ErrLocalCleanup)
		}
	}()
	for _, r := range renditions {
		r.DurationSeconds, r.SegmentCount = 2, 1
		output := filepath.Join(directory, r.Name)
		if err := os.Mkdir(output, 0700); err != nil {
			return err
		}
		args, err := encodeArguments(filepath.Join(directory, "synthetic.mp4"), output, r, encoder)
		if err != nil {
			return err
		}
		start, end := slices.Index(args, "-protocol_whitelist"), slices.Index(args, "-i")
		input := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%g", r.Width, r.Height, r.FrameRate), "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2"}
		args = append(append(append([]string{}, args[:start]...), input...), args[end+2:]...)
		for i, arg := range args {
			if arg == "0:a:0" {
				args[i] = "1:a:0"
			}
		}
		if _, err := operation.RunTool(ctx, ffmpeg, args, 1<<20); err != nil {
			return err
		}
		if _, err := MeasureRendition(ctx, ffprobe, output, r); err != nil {
			return err
		}
	}
	return nil
}

func qualificationError(outer, probe context.Context, err error) error {
	if errors.Is(err, ErrLocalCleanup) {
		return err
	}
	if outer.Err() != nil {
		return outer.Err()
	}
	if probe.Err() == context.DeadlineExceeded {
		return operation.ErrProcessFailed
	}
	return err
}
