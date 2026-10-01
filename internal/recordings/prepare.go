package recordings

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func preparedInput(ctx context.Context, j *Journal) (string, error) {
	if !j.state.Intent.Prepare {
		return j.state.Intent.Input, nil
	}
	directory, err := j.generatedWorkspace()
	if err != nil {
		return "", err
	}
	output := filepath.Join(directory, "package")
	if info, err := os.Lstat(output); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || j.state.SourceFingerprint.SHA256 == "" {
			return "", ErrOperationConflict
		}
		// Upload rehashes the complete inventory before any remote mutation.
		return output, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tools, err := bundle.Verify()
	if err != nil {
		return "", err
	}
	if err := j.cleanGenerated(); err != nil {
		return "", err
	}
	if _, err := j.generatedWorkspace(); err != nil {
		return "", err
	}
	options := media.DefaultEncodeOptions()
	options.VideoBitrate720, options.VideoBitrate1080 = j.state.Intent.VideoBitrate720, j.state.Intent.VideoBitrate1080
	_, err = media.PrepareCPU(ctx, j.state.Intent.Input, output, tools.FFmpeg, tools.FFprobe, options, func(f media.SourceFingerprint) error {
		state := j.State()
		state.SourceFingerprint = f
		return j.Save(state)
	})
	if err != nil {
		if cleanupErr := j.cleanGenerated(); cleanupErr != nil {
			err = errors.Join(err, media.ErrLocalCleanup)
		}
		return "", err
	}
	return output, nil
}
