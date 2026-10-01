package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func artifactName(version, platform string) (string, error) {
	if _, err := update.Newer(version, version); err != nil {
		return "", err
	}
	ext := ".tar.gz"
	switch platform {
	case "windows/amd64":
		ext = ".zip"
	case "darwin/arm64":
	default:
		return "", update.ErrInvalidRelease
	}
	return "hhc_" + version + "_" + strings.ReplaceAll(platform, "/", "_") + ext, nil
}

func artifactMetadata(path, version, platform string) (update.Artifact, error) {
	name, err := artifactName(version, platform)
	if err != nil || filepath.Base(path) != name {
		return update.Artifact{}, update.ErrInvalidRelease
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > update.MaxArtifactBytes {
		return update.Artifact{}, update.ErrInvalidRelease
	}
	f, err := os.Open(path)
	if err != nil {
		return update.Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(f, update.MaxArtifactBytes+1), make([]byte, 64<<10))
	if err != nil || n != info.Size() {
		return update.Artifact{}, update.ErrInvalidRelease
	}
	return update.Artifact{Platform: platform, URL: "https://github.com/HallelujahHomeChurch/hhc-cli/releases/download/v" + version + "/" + name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func archiveRelease(directory, output, version, platform string) (result string, err error) {
	name, err := artifactName(version, platform)
	if err != nil {
		return "", err
	}
	result = filepath.Join(output, name)
	f, err := os.OpenFile(result, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			err = errors.Join(err, os.Remove(result))
		}
	}()
	var zw *zip.Writer
	var gz *gzip.Writer
	var tw *tar.Writer
	if platform == "windows/amd64" {
		zw = zip.NewWriter(f)
	} else {
		gz = gzip.NewWriter(f)
		tw = tar.NewWriter(gz)
	}
	count := 0
	err = filepath.WalkDir(directory, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<30 {
			return update.ErrInvalidRelease
		}
		count++
		if count > 1000 {
			return update.ErrInvalidRelease
		}
		rel, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		var out io.Writer
		if zw != nil {
			header := &zip.FileHeader{Name: rel, Method: zip.Deflate}
			header.SetMode(info.Mode().Perm())
			out, err = zw.CreateHeader(header)
		} else {
			err = tw.WriteHeader(&tar.Header{Name: rel, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)})
			out = tw
		}
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		n, err := io.CopyBuffer(out, io.LimitReader(in, info.Size()+1), make([]byte, 64<<10))
		if err != nil {
			return err
		}
		if n != info.Size() {
			return update.ErrInvalidRelease
		}
		return nil
	})
	if zw != nil {
		err = errors.Join(err, zw.Close())
	} else {
		err = errors.Join(err, tw.Close(), gz.Close())
	}
	if err != nil {
		return result, err
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	info, err := f.Stat()
	if err != nil || info.Size() > update.MaxArtifactBytes {
		return result, fmt.Errorf("release archive exceeds size limit")
	}
	return result, nil
}
