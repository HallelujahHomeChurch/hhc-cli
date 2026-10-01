package recordings

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

type PreparationResult struct {
	OperationID              string                  `json:"operationId"`
	OutputPath               string                  `json:"outputPath"`
	PackageDigest            string                  `json:"packageDigest"`
	SizeBytes                int64                   `json:"sizeBytes"`
	SourceFingerprint        media.SourceFingerprint `json:"sourceFingerprint"`
	Encoding                 *media.EncodingSummary  `json:"encoding,omitempty"`
	PrepareState             string                  `json:"prepareState"`
	LocalCleanupState        string                  `json:"localCleanupState"`
	RequestedActionSatisfied bool                    `json:"requestedActionSatisfied"`
}

// Prepare retains the explicit output. Only its separately owned workspace is
// disposable, and it lives on the output volume for atomic no-replace rename.
func Prepare(ctx context.Context, j *Journal) (value PreparationResult, err error) {
	state := j.State()
	value = PreparationResult{OperationID: state.OperationID, OutputPath: state.Intent.Output, PrepareState: "pending", LocalCleanupState: "pending"}
	if state.Intent.Command != "prepare" {
		return value, ErrOperationConflict
	}
	if err := ctx.Err(); err != nil {
		return value, err
	}
	output := state.Intent.Output
	if _, statErr := os.Lstat(output); statErr == nil {
		if state.PackageDigest == "" || state.SourceFingerprint.SHA256 == "" || !state.GeneratedOwned {
			return value, ErrOperationConflict
		}
		inv, err := media.ReadPackage(ctx, output)
		if err != nil {
			return value, err
		}
		if inv.InventoryDigest != state.PackageDigest {
			return value, ErrPackageChanged
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return value, statErr
	} else {
		directory, err := j.generatedWorkspace()
		if err != nil {
			return value, err
		}
		staging := filepath.Join(directory, ".hhc-prepare-package")
		if _, statErr := os.Lstat(staging); errors.Is(statErr, os.ErrNotExist) {
			tools, err := bundle.Verify()
			if err != nil {
				return value, err
			}
			if err := j.cleanGenerated(); err != nil {
				return value, err
			}
			if _, err := j.generatedWorkspace(); err != nil {
				return value, err
			}
			options := media.DefaultEncodeOptions()
			options.Progress = j.Progress
			options.VideoBitrate720, options.VideoBitrate1080 = state.Intent.VideoBitrate720, state.Intent.VideoBitrate1080
			prepared, err := media.PrepareAuto(ctx, state.Intent.Input, staging, tools.FFmpeg, tools.FFprobe, options, func(f media.SourceFingerprint) error {
				next := j.State()
				next.SourceFingerprint = f
				return j.Save(next)
			})
			if err != nil {
				if cleanupErr := j.cleanGenerated(); cleanupErr != nil {
					err = errors.Join(err, media.ErrLocalCleanup)
				}
				return value, err
			}
			next := j.State()
			next.Encoding = &prepared.EncodingSummary
			if err := j.Save(next); err != nil {
				return value, err
			}
		} else if statErr != nil {
			return value, statErr
		}
		state = j.State()
		if state.SourceFingerprint.SHA256 == "" {
			return value, ErrOperationConflict
		}
		inv, err := media.ReadPackage(ctx, staging)
		if err != nil {
			return value, err
		}
		state.PackageDigest = inv.InventoryDigest
		state.PackageBytes, _ = media.ValidateRecordingInventory(inv)
		if err := j.Save(state); err != nil {
			return value, err
		}
		if err := operation.FinalizeDirectory(staging, output); err != nil {
			return value, err
		}
	}
	state = j.State()
	value.PackageDigest, value.SizeBytes, value.SourceFingerprint = state.PackageDigest, state.PackageBytes, state.SourceFingerprint
	value.Encoding = state.Encoding
	value.PrepareState = "complete"
	if err := j.cleanGenerated(); err != nil {
		value.LocalCleanupState = "failed"
		return value, errors.Join(media.ErrLocalCleanup, err)
	}
	value.LocalCleanupState, value.RequestedActionSatisfied = "complete", true
	return value, nil
}
