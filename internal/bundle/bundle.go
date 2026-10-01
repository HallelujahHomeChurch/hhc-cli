// Package bundle verifies the tools pinned into the CLI release itself.
package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

var ErrUnavailable = errors.New("ffmpeg_bundle_unavailable")

// Set only by the release build using -X. No environment/CLI/PATH override:
// unpacked manifest files cannot replace this embedded integrity anchor.
var manifestBase64 string

type File struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	BundleVersion string `json:"bundleVersion"`
	Platform      string `json:"platform"`
	FFmpeg        File   `json:"ffmpeg"`
	FFprobe       File   `json:"ffprobe"`
}

type Tools struct {
	FFmpeg  string
	FFprobe string
	Version string
}

func Verify() (Tools, error) {
	executable, err := os.Executable()
	if err != nil {
		return Tools{}, ErrUnavailable
	}
	return verifyAt(filepath.Join(filepath.Dir(executable), "ffmpeg"), manifestBase64)
}

func verifyAt(directory, encoded string) (Tools, error) {
	if !filepath.IsAbs(directory) || len(encoded) == 0 || len(encoded) > 32<<10 {
		return Tools{}, ErrUnavailable
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > 16<<10 {
		return Tools{}, ErrUnavailable
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil {
		return Tools{}, ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || manifest.SchemaVersion != 1 || manifest.Platform != runtime.GOOS+"/"+runtime.GOARCH || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,63}$`).MatchString(manifest.BundleVersion) {
		return Tools{}, ErrUnavailable
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Tools{}, ErrUnavailable
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Tools{}, ErrUnavailable
	}
	defer root.Close()
	result := Tools{Version: manifest.BundleVersion}
	for _, item := range []struct {
		name     string
		expected File
		path     *string
	}{{"ffmpeg", manifest.FFmpeg, &result.FFmpeg}, {"ffprobe", manifest.FFprobe, &result.FFprobe}} {
		name := item.name
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if !verifyFile(root, name, item.expected) {
			return Tools{}, ErrUnavailable
		}
		*item.path = filepath.Join(directory, name)
	}
	return result, nil
}

func verifyFile(root *os.Root, name string, expected File) bool {
	digest, err := hex.DecodeString(expected.SHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != expected.SHA256 || expected.SizeBytes <= 0 || expected.SizeBytes > 512<<20 {
		return false
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected.SizeBytes || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return false
	}
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, io.LimitReader(f, expected.SizeBytes+1), make([]byte, 64<<10))
	if err != nil || count != expected.SizeBytes || !bytes.Equal(hash.Sum(nil), digest) {
		return false
	}
	after, err := f.Stat()
	return err == nil && after.Size() == info.Size() && after.ModTime().Equal(info.ModTime())
}
