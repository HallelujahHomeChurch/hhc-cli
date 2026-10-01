package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BuildPackageInventory measures a finished, operation-owned staging directory.
// It does not write package.json or establish media readiness; the caller must
// validate actual codecs/timeline and prevent concurrent staging mutations.
func BuildPackageInventory(ctx context.Context, directory string, renditions []RecordingRendition, presetVersion string) (RecordingPackageInventory, error) {
	return buildPackageInventory(ctx, directory, renditions, presetVersion, false)
}

// ReadPackage verifies the complete local inventory without changing user files.
// Upload still rechecks each opened object immediately before sending bytes.
func ReadPackage(ctx context.Context, directory string) (RecordingPackageInventory, error) {
	if !filepath.IsAbs(directory) {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	defer root.Close()
	info, err := root.Lstat("package.json")
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > RecordingInventoryMaxBytes {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	f, err := root.Open("package.json")
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	inv, err := DecodeRecordingInventory(f)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	actual, err := buildPackageInventory(ctx, directory, inv.Renditions, inv.PresetVersion, true)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	if actual.InventoryDigest != inv.InventoryDigest {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	return actual, nil
}

func buildPackageInventory(ctx context.Context, directory string, renditions []RecordingRendition, presetVersion string, manifest bool) (RecordingPackageInventory, error) {
	if !filepath.IsAbs(directory) || len(renditions) < 1 || len(renditions) > 2 || !presetVersionPattern.MatchString(presetVersion) {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	allowedDirs := map[string]bool{}
	directories := []string{"."}
	for _, r := range renditions {
		if !validRecordingRendition(r) || allowedDirs[r.Name] {
			return RecordingPackageInventory{}, ErrInvalidInput
		}
		allowedDirs[r.Name] = true
		directories = append(directories, r.Name)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RecordingPackageInventory{}, ErrInvalidInput
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	defer root.Close()
	inv := RecordingPackageInventory{SchemaVersion: 1, PresetVersion: presetVersion, Renditions: slices.Clone(renditions)}
	var total int64
	for _, name := range directories {
		if err := ctx.Err(); err != nil {
			return RecordingPackageInventory{}, err
		}
		dirInfo, err := root.Lstat(name)
		if err != nil {
			return RecordingPackageInventory{}, err
		}
		if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
			return RecordingPackageInventory{}, ErrInvalidInput
		}
		dir, err := root.Open(name)
		if err != nil {
			return RecordingPackageInventory{}, err
		}
		openedInfo, err := dir.Stat()
		if err != nil || !os.SameFile(dirInfo, openedInfo) {
			dir.Close()
			return RecordingPackageInventory{}, ErrInvalidInput
		}
		for {
			entries, readErr := dir.ReadDir(100)
			if readErr != nil && readErr != io.EOF {
				dir.Close()
				return RecordingPackageInventory{}, readErr
			}
			for _, entry := range entries {
				if manifest && name == "." && entry.Name() == "package.json" && entry.Type().IsRegular() {
					continue
				}
				if name == "." && entry.IsDir() && allowedDirs[entry.Name()] {
					continue
				}
				path := entry.Name()
				if name != "." {
					path = name + "/" + path
				}
				if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || len(inv.Objects) >= RecordingPackageMaxObjects {
					dir.Close()
					return RecordingPackageInventory{}, ErrInvalidInput
				}
				object, err := hashPackageObject(ctx, root, path)
				if err != nil {
					dir.Close()
					return RecordingPackageInventory{}, err
				}
				total += object.SizeBytes
				if total > RecordingPackageMaxBytes {
					dir.Close()
					return RecordingPackageInventory{}, ErrRecordingPackageTooLarge
				}
				inv.Objects = append(inv.Objects, object)
			}
			if readErr == io.EOF {
				break
			}
		}
		if err := dir.Close(); err != nil {
			return RecordingPackageInventory{}, err
		}
	}
	slices.SortFunc(inv.Objects, func(a, b RecordingPackageObject) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(inv.Renditions, func(a, b RecordingRendition) int { return strings.Compare(a.Name, b.Name) })
	inv.InventoryDigest, err = RecordingInventoryDigest(inv)
	if err != nil {
		return RecordingPackageInventory{}, err
	}
	if _, err := ValidateRecordingInventory(inv); err != nil {
		return RecordingPackageInventory{}, err
	}
	return inv, nil
}

func hashPackageObject(ctx context.Context, root *os.Root, path string) (RecordingPackageObject, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return RecordingPackageObject{}, err
	}
	maxSize := RecordingObjectMaxBytes
	if strings.HasSuffix(path, ".m3u8") {
		maxSize = RecordingPlaylistMaxBytes
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSize {
		return RecordingPackageObject{}, ErrInvalidInput
	}
	f, err := root.Open(path)
	if err != nil {
		return RecordingPackageObject{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return RecordingPackageObject{}, ErrInvalidInput
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return RecordingPackageObject{}, err
		}
		n, err := f.Read(buffer)
		size += int64(n)
		if size > maxSize {
			return RecordingPackageObject{}, ErrInvalidInput
		}
		hash.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return RecordingPackageObject{}, err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return RecordingPackageObject{}, err
	}
	if size != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return RecordingPackageObject{}, ErrInvalidInput
	}
	return RecordingPackageObject{Path: path, SizeBytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
