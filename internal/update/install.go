package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

type Pointer struct {
	SchemaVersion int    `json:"schemaVersion"`
	Current       string `json:"current"`
	Previous      string `json:"previous,omitempty"`
}

func ReadPointer(directory string) (Pointer, error) {
	var p Pointer
	if !filepath.IsAbs(directory) {
		return p, ErrInvalidRelease
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return p, err
	}
	defer root.Close()
	info, err := root.Lstat("current.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return p, ErrInvalidRelease
	}
	f, err := root.Open("current.json")
	if err != nil {
		return p, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return p, ErrInvalidRelease
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&p) != nil || d.Decode(&extra) != io.EOF || p.SchemaVersion != 1 || !stableVersion.MatchString(p.Current) || p.Previous != "" && !stableVersion.MatchString(p.Previous) {
		return Pointer{}, ErrInvalidRelease
	}
	return p, nil
}

// installDownloaded is called only after manifest authentication. The exclusive
// installation lock covers self-check and pointer swap; credentials and operation
// journals are outside this tree and are never opened by installation.
func installDownloaded(ctx context.Context, directory string, m Manifest, a Artifact, archive string, check func(context.Context, string, Manifest) error) (err error) {
	lock, err := operation.LockWorkspace(directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	return installLocked(ctx, directory, m, a, archive, check)
}

func installLocked(ctx context.Context, directory string, m Manifest, a Artifact, archive string, check func(context.Context, string, Manifest) error) (err error) {
	previous, err := ReadPointer(directory)
	if err != nil {
		return err
	}
	newer, err := Newer(m.Version, previous.Current)
	if err != nil {
		return err
	}
	if !newer {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	versions := filepath.Join(directory, "versions")
	info, err := os.Lstat(versions)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidRelease
	}
	stage, err := os.MkdirTemp(versions, ".install-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	candidate := filepath.Join(stage, ".hhc-prepare-release")
	if err = ExtractBundle(archive, candidate, a); err != nil {
		return err
	}
	if err = check(ctx, candidate, m); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(versions, m.Version)
	created := false
	if _, statErr := os.Lstat(target); statErr == nil {
		// Recover a crash after finalization but before pointer switching. Match
		// every byte/path against a newly authenticated extraction; never execute,
		// replace or delete an unknown existing directory.
		expected, err := bundleDigests(candidate)
		if err != nil {
			return err
		}
		actual, err := bundleDigests(target)
		if err != nil || !maps.Equal(expected, actual) {
			return ErrInvalidRelease
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else {
		if err = operation.FinalizeDirectory(candidate, target); err != nil {
			return err
		}
		created = true
	}
	// ponytail: retain one bundle per update to avoid racing a launcher that
	// selected an older version. Add lease-aware pruning if disk growth matters.
	next := Pointer{SchemaVersion: 1, Current: m.Version, Previous: previous.Current}
	if err = writePointer(directory, next); err != nil {
		// A post-rename durability failure has an ambiguous result. Restore the
		// prior pointer before reporting failure; do not delete a possibly-current
		// candidate if the rollback also fails.
		rollbackErr := writePointer(directory, previous)
		if rollbackErr == nil && created {
			return errors.Join(err, os.RemoveAll(target))
		}
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func bundleDigests(directory string) (map[string]string, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidRelease
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := make(map[string]string)
	var total int64
	var walk func(string) error
	walk = func(name string) error {
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, readErr := dir.ReadDir(64)
			for _, entry := range entries {
				if len(result) >= maxExtractedFiles*2 {
					return ErrInvalidRelease
				}
				path := filepath.Join(name, entry.Name())
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.IsDir() {
					result[path] = "directory"
					if err := walk(path); err != nil {
						return err
					}
					continue
				}
				if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 1<<30 || total > maxExtractedBytes-info.Size() {
					return ErrInvalidRelease
				}
				total += info.Size()
				f, err := root.Open(path)
				if err != nil {
					return err
				}
				h := sha256.New()
				n, copyErr := io.CopyBuffer(h, io.LimitReader(f, info.Size()+1), make([]byte, 64<<10))
				f.Close()
				if copyErr != nil || n != info.Size() {
					return ErrInvalidRelease
				}
				result[path] = hex.EncodeToString(h.Sum(nil))
				if runtime.GOOS != "windows" {
					result[path] += "/" + strconv.FormatUint(uint64(info.Mode().Perm()), 8)
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return result, nil
}

func writePointer(directory string, p Pointer) (err error) {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".current-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		f.Close()
		if _, statErr := os.Lstat(name); statErr == nil {
			err = errors.Join(err, os.Remove(name))
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replacePointer(directory, name)
}
