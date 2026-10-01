package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func packageRelease(ctx context.Context, output, media, version, publicKey string) (err error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if _, err := artifactName(version, platform); err != nil {
		return err
	}
	key, err := hex.DecodeString(publicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || !filepath.IsAbs(output) || !filepath.IsAbs(media) {
		return update.ErrInvalidRelease
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(output, ".package-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	if err := os.CopyFS(stage, os.DirFS(media)); err != nil {
		return err
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	m := bundle.Manifest{SchemaVersion: 1, BundleVersion: mediaBundleVersion, Platform: platform}
	for _, tool := range []struct {
		name string
		file *bundle.File
	}{{"ffmpeg", &m.FFmpeg}, {"ffprobe", &m.FFprobe}} {
		path := filepath.Join(stage, "ffmpeg", tool.name+suffix)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 {
			return update.ErrInvalidRelease
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(file, (512<<20)+1))
		if err := errors.Join(copyErr, file.Close()); err != nil || n != info.Size() {
			return update.ErrInvalidRelease
		}
		*tool.file = bundle.File{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: n}
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		return err
	}
	module := "github.com/HallelujahHomeChurch/hhc-cli/"
	flags := "-X main.version=" + version + " -X " + module + "internal/bundle.manifestBase64=" + base64.StdEncoding.EncodeToString(manifest) + " -X " + module + "internal/update.publicKeyHex=" + hex.EncodeToString(key)
	for _, name := range []string{"hhc", "hhc-launcher"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", flags, "-o", filepath.Join(stage, name+suffix), "./cmd/"+name)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	for _, path := range []string{"skills/hhc/SKILL.md", "skills/hhc/references/commands.md", "README.md"} {
		if err := copyNotice(path, filepath.Join(stage, path)); err != nil {
			return err
		}
	}
	goLicense := filepath.Join(runtime.GOROOT(), "LICENSE")
	if _, err := os.Stat(goLicense); errors.Is(err, os.ErrNotExist) {
		// Homebrew keeps the distribution license beside its libexec GOROOT.
		goLicense = filepath.Join(filepath.Dir(runtime.GOROOT()), "LICENSE")
	}
	if err := copyNotice(goLicense, filepath.Join(stage, "licenses", "Go-LICENSE.txt")); err != nil {
		return err
	}
	for _, name := range []string{"sys", "term"} {
		data, err := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "golang.org/x/"+name).Output()
		if err != nil {
			return err
		}
		if err := copyNotice(filepath.Join(strings.TrimSpace(string(data)), "LICENSE"), filepath.Join(stage, "licenses", "x-"+name+"-LICENSE.txt")); err != nil {
			return err
		}
	}
	if err := writeNew(filepath.Join(stage, "bundle.json"), manifest, 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(stage, "hhc"+suffix), "version", "--self-check", "--json")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	archive, err := archiveRelease(stage, output, version, platform)
	if err != nil {
		return err
	}
	a, err := artifactMetadata(archive, version, platform)
	if err != nil {
		return err
	}
	return update.ExtractBundle(archive, filepath.Join(stage, "verify-extraction"), a)
}

func copyNotice(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return writeNew(destination, data, 0600)
}
