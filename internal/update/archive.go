package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const maxExtractedBytes int64 = 4 << 30
const maxExtractedFiles = 1000

var archiveComponent = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)

// ExtractBundle takes an artifact from VerifyManifest. It validates the archive
// hash before extraction, creates only a new private directory and never runs
// its contents. An existing destination is always preserved.
func ExtractBundle(archive, destination string, artifact Artifact) (err error) {
	if !filepath.IsAbs(destination) || filepath.Dir(destination) == destination || artifact.Size <= 0 || artifact.Size > MaxArtifactBytes || (artifact.Platform != "windows/amd64" && artifact.Platform != "darwin/arm64") {
		return ErrInvalidRelease
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return ErrInvalidRelease
	}
	h := sha256.New()
	if n, err := io.CopyBuffer(h, io.LimitReader(f, artifact.Size+1), make([]byte, 64<<10)); err != nil || n != artifact.Size || hex.EncodeToString(h.Sum(nil)) != artifact.SHA256 {
		return ErrInvalidRelease
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(destination))
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := make(map[string]bool)
	var total int64
	suffix := ""
	if artifact.Platform == "windows/amd64" {
		suffix = ".exe"
	}
	required := map[string]bool{"hhc" + suffix: false, "hhc-launcher" + suffix: false, "ffmpeg/ffmpeg" + suffix: false, "ffmpeg/ffprobe" + suffix: false, "skills/hhc/SKILL.md": false, "skills/hhc/references/commands.md": false}
	write := func(name string, size int64, directory bool, r io.Reader) error {
		if directory {
			name = strings.TrimSuffix(name, "/")
		}
		if !validArchivePath(name) || len(seen) >= maxExtractedFiles || seen[strings.ToLower(name)] {
			return ErrInvalidRelease
		}
		seen[strings.ToLower(name)] = true
		allowed := false
		if directory {
			allowed = name == "ffmpeg" || name == "skills" || name == "skills/hhc" || name == "skills/hhc/references" || name == "licenses" || name == "source"
		} else {
			_, allowed = required[name]
			allowed = allowed || strings.HasPrefix(name, "licenses/") || strings.HasPrefix(name, "source/") || name == "bundle.json" || name == "README.md"
		}
		if !allowed {
			return ErrInvalidRelease
		}
		if directory {
			if size != 0 {
				return ErrInvalidRelease
			}
			return root.MkdirAll(filepath.FromSlash(name), 0700)
		}
		if size < 0 || size > 1<<30 || total > maxExtractedBytes-size {
			return ErrInvalidRelease
		}
		total += size
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(name)), 0700); err != nil {
			return err
		}
		mode := fs.FileMode(0600)
		if name == "hhc"+suffix || name == "hhc-launcher"+suffix || name == "ffmpeg/ffmpeg"+suffix || name == "ffmpeg/ffprobe"+suffix {
			mode = 0700
		}
		out, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		n, copyErr := io.CopyBuffer(out, io.LimitReader(r, size+1), make([]byte, 64<<10))
		syncErr := out.Sync()
		closeErr := out.Close()
		if n != size || copyErr != nil || syncErr != nil || closeErr != nil {
			return errors.Join(ErrInvalidRelease, copyErr, syncErr, closeErr)
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
		return nil
	}
	if artifact.Platform == "windows/amd64" {
		reader, err := zip.NewReader(f, artifact.Size)
		if err != nil || len(reader.File) > maxExtractedFiles {
			return ErrInvalidRelease
		}
		for _, entry := range reader.File {
			if !entry.Mode().IsRegular() && !entry.Mode().IsDir() || entry.UncompressedSize64 > 1<<30 {
				return ErrInvalidRelease
			}
			input, err := entry.Open()
			if err != nil {
				return ErrInvalidRelease
			}
			writeErr := write(entry.Name, int64(entry.UncompressedSize64), entry.Mode().IsDir(), input)
			closeErr := input.Close()
			if writeErr != nil || closeErr != nil {
				return errors.Join(ErrInvalidRelease, writeErr, closeErr)
			}
		}
	} else {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return ErrInvalidRelease
		}
		defer gz.Close()
		// Bound uncompressed padding as well as file content.
		bounded := &io.LimitedReader{R: gz, N: maxExtractedBytes + maxExtractedFiles*4096}
		reader := tar.NewReader(bounded)
		for {
			entry, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return ErrInvalidRelease
			}
			if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeDir {
				return ErrInvalidRelease
			}
			if err = write(entry.Name, entry.Size, entry.Typeflag == tar.TypeDir, reader); err != nil {
				return err
			}
		}
		// Force gzip checksum verification even when tar reached its end marker.
		if _, err := io.Copy(io.Discard, bounded); err != nil || bounded.N <= 0 {
			return ErrInvalidRelease
		}
	}
	for _, present := range required {
		if !present {
			return ErrInvalidRelease
		}
	}
	return nil
}

func validArchivePath(name string) bool {
	if !fs.ValidPath(name) || name == "." || len(name) > 240 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !archiveComponent.MatchString(part) || strings.HasSuffix(part, ".") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return false
		}
	}
	return true
}
