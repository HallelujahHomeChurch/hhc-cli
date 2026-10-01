package media

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

var ErrInsufficientDisk = errors.New("insufficient_disk_space")
var ErrLocalCleanup = errors.New("local_cleanup_failed")

type PreparedPackage struct {
	Inventory         RecordingPackageInventory `json:"inventory"`
	SourceFingerprint SourceFingerprint         `json:"sourceFingerprint"`
	OutputPath        string                    `json:"outputPath"`
	ActualEncoder     string                    `json:"actualEncoder"`
	PresetVersion     string                    `json:"presetVersion"`
	NearCapacity      bool                      `json:"nearCapacity"`
}

// PrepareCPU is the fixed CPU fallback pipeline, not an encoder selector.
// The caller verifies bundled tools and places output in its journal-owned
// workspace. Nothing here selects PATH binaries, deletes source, or replaces
// existing output. Publication/upload and generated-package cleanup are separate.
func PrepareCPU(ctx context.Context, source, output, ffmpeg, ffprobe string, options EncodeOptions, checkpoint func(SourceFingerprint) error) (value PreparedPackage, err error) {
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(output) || !filepath.IsAbs(ffmpeg) || !filepath.IsAbs(ffprobe) || source == output {
		return value, ErrInvalidInput
	}
	info, err := os.Lstat(source)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() {
		return value, ErrUnsupportedSource
	}
	if err := ValidateRecordingSourceSize(info.Size()); err != nil {
		return value, err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return value, ErrInvalidInput
	}
	parent := filepath.Dir(output)
	available, err := operation.AvailableBytes(parent)
	if err != nil {
		return value, err
	}
	if available < 256<<20 {
		return value, ErrInsufficientDisk
	}
	scratch, err := os.MkdirTemp(parent, ".hhc-source-")
	if err != nil {
		return value, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(scratch); cleanupErr != nil {
			err = errors.Join(err, ErrLocalCleanup)
		}
	}()
	stable, err := operation.OpenStableSource(ctx, source, scratch)
	if err != nil {
		return value, err
	}
	defer stable.Close()
	value.SourceFingerprint, err = FingerprintSource(ctx, stable)
	if err != nil {
		return value, err
	}
	// Persist identity before encoding. A resumed operation must not silently
	// upload a different recording after its original source was changed.
	if checkpoint != nil {
		if err := checkpoint(value.SourceFingerprint); err != nil {
			return value, err
		}
	}
	metadata, err := ProbeSource(ctx, ffprobe, stable.Name())
	if err != nil {
		return value, err
	}
	plan, err := PlanSource(metadata, options)
	if err != nil {
		return value, err
	}
	reserve := uint64(plan.EstimatedBytes) + (512 << 20)
	if runtime.GOOS == "darwin" {
		reserve += uint64(info.Size())
	} // Worst-case CoW divergence; do not count a clone as permanently free.
	available, err = operation.AvailableBytes(parent)
	if err != nil {
		return value, err
	}
	if available < reserve {
		return value, ErrInsufficientDisk
	}
	staging, err := os.MkdirTemp(parent, ".hhc-prepare-")
	if err != nil {
		return value, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(staging); cleanupErr != nil {
			err = errors.Join(err, ErrLocalCleanup)
		}
	}()
	workCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func(stop <-chan struct{}) {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if err := checkStagingBudget(staging); err != nil {
					cancel(err)
					return
				}
				free, err := operation.AvailableBytes(parent)
				if err != nil {
					cancel(err)
					return
				}
				if free < 256<<20 {
					cancel(ErrInsufficientDisk)
					return
				}
			}
		}
	}(stop)
	stopMonitor := func() {
		if stop != nil {
			close(stop)
			<-done
			stop = nil
		}
	}
	defer stopMonitor()
	var measured []RenditionMedia
	for _, r := range plan.Renditions {
		directory := filepath.Join(staging, r.Name)
		if err := os.Mkdir(directory, 0700); err != nil {
			return value, err
		}
		args, err := CPUEncodeArguments(stable.Name(), directory, r)
		if err != nil {
			return value, err
		}
		if _, err := operation.RunTool(workCtx, ffmpeg, args, 1<<20); err != nil {
			return value, err
		}
		actual, err := MeasureRendition(workCtx, ffprobe, directory, r)
		if err != nil {
			return value, err
		}
		measured = append(measured, actual)
	}
	master, err := BuildMasterPlaylist(measured)
	if err != nil {
		return value, err
	}
	root, err := os.OpenRoot(staging)
	if err != nil {
		return value, err
	}
	defer root.Close()
	if err := writePreparedFile(root, "master.m3u8", master); err != nil {
		return value, err
	}
	value.Inventory, err = BuildPackageInventory(workCtx, staging, plan.Renditions, "cpu-hq-v1")
	if err != nil {
		return value, err
	}
	manifest, err := json.Marshal(value.Inventory)
	if err != nil {
		return value, err
	}
	if err := writePreparedFile(root, "package.json", manifest); err != nil {
		return value, err
	}
	for _, object := range value.Inventory.Objects {
		file, err := root.OpenFile(object.Path, os.O_RDWR, 0)
		if err != nil {
			return value, err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return value, err
		}
	}
	stopMonitor()
	if err := context.Cause(workCtx); err != nil {
		return value, err
	}
	if err := checkStagingBudget(staging); err != nil {
		return value, err
	}
	root.Close() // Windows cannot move a directory still held by this process.
	if err := operation.FinalizeDirectory(staging, output); err != nil {
		return value, err
	}
	value.OutputPath, value.ActualEncoder, value.PresetVersion, value.NearCapacity = output, "libx264", "cpu-hq-v1", plan.NearCapacity
	return value, nil
}

func writePreparedFile(root *os.Root, path string, data []byte) error {
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func checkStagingBudget(directory string) error {
	var size int64
	count := 0
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrInvalidInput
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrInvalidInput
		}
		size += info.Size()
		count++
		if size > RecordingPackageMaxBytes || count > RecordingPackageMaxObjects+8 {
			return ErrRecordingPackageTooLarge
		}
		return nil
	})
}
