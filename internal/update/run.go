package update

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

var ErrManagedInstallRequired = errors.New("managed_install_required")
var ErrTrustUnavailable = errors.New("release_trust_unavailable")

// Release CI embeds the public key. There is deliberately no environment or
// downloaded-file override and no fallback to unsigned checksums.
var publicKeyHex string

type Result struct {
	CurrentVersion string `json:"currentVersion"`
	TargetVersion  string `json:"targetVersion"`
	Platform       string `json:"platform"`
	Available      bool   `json:"available"`
	Installed      bool   `json:"installed"`
	SkillVersion   string `json:"skillVersion"`
}

func ManagedDirectory(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", ErrManagedInstallRequired
	}
	versionDir := filepath.Dir(executable)
	versions := filepath.Dir(versionDir)
	if filepath.Base(versions) != "versions" {
		return "", ErrManagedInstallRequired
	}
	if !stableVersion.MatchString(filepath.Base(versionDir)) {
		return "", ErrInvalidRelease
	}
	for _, dir := range []string{versionDir, versions} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrInvalidRelease
		}
	}
	directory := filepath.Dir(versions)
	if _, err := ReadPointer(directory); err != nil {
		return "", err
	}
	return directory, nil
}

// LockCommand is held by managed CLI commands for their full lifetime. Portable
// commands do not participate in managed installation and remain usable.
func LockCommand(executable string) (*os.File, error) {
	dir, err := ManagedDirectory(executable)
	if errors.Is(err, ErrManagedInstallRequired) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return operation.LockSharedWorkspace(dir)
}

func Execute(ctx context.Context, current string, checkOnly bool) (value Result, err error) {
	value.CurrentVersion, value.Platform = current, runtime.GOOS+"/"+runtime.GOARCH
	if err := ctx.Err(); err != nil {
		return value, err
	}
	var directory string
	if !checkOnly {
		exe, err := os.Executable()
		if err != nil {
			return value, ErrManagedInstallRequired
		}
		directory, err = ManagedDirectory(exe)
		if err != nil {
			return value, err
		}
		lock, err := operation.LockWorkspace(directory)
		if err != nil {
			return value, err
		}
		defer lock.Close()
		pointer, err := ReadPointer(directory)
		if err != nil {
			return value, err
		}
		current = pointer.Current
		value.CurrentVersion = current
	}
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return value, ErrTrustUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	client := newReleaseClient()
	m, newer, err := checkRelease(ctx, client, ed25519.PublicKey(key), value.Platform, current)
	if err != nil {
		return value, err
	}
	if !newer && m.Version != current {
		return value, ErrInvalidRelease
	}
	value.TargetVersion, value.Available, value.SkillVersion = m.Version, newer, m.SkillVersion
	if checkOnly {
		return value, nil
	}
	if !newer {
		value.Installed = true
		return value, nil
	}
	var artifact Artifact
	for _, a := range m.Artifacts {
		if a.Platform == value.Platform {
			artifact = a
		}
	}
	temp, err := os.MkdirTemp(directory, ".download-")
	if err != nil {
		return value, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	archive := filepath.Join(temp, "release.archive")
	if err = downloadArtifact(ctx, client, artifact, archive); err != nil {
		return value, err
	}
	if err = installLocked(ctx, directory, m, artifact, archive, selfCheck); err != nil {
		return value, err
	}
	value.Installed = true
	return value, nil
}

func selfCheck(ctx context.Context, directory string, m Manifest) error {
	name := "hhc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	data, err := operation.RunTool(ctx, filepath.Join(directory, name), []string{"version", "--self-check", "--json"}, 4096)
	if err != nil {
		return ErrInvalidRelease
	}
	var response struct {
		SchemaVersion int  `json:"schemaVersion"`
		OK            bool `json:"ok"`
		Data          struct {
			Version       string `json:"version"`
			Platform      string `json:"platform"`
			JournalSchema int    `json:"journalSchema"`
			SkillVersion  string `json:"skillVersion"`
			BundleVersion string `json:"bundleVersion"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &response) != nil || !response.OK || response.SchemaVersion != 1 || response.Data.Version != m.Version || response.Data.Platform != runtime.GOOS+"/"+runtime.GOARCH || response.Data.JournalSchema != m.JournalSchema || response.Data.SkillVersion != m.SkillVersion || response.Data.BundleVersion != m.BundleVersion {
		return ErrInvalidRelease
	}
	return nil
}
