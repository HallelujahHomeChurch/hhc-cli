package update

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func Bootstrap(ctx context.Context, directory, current string) (value Result, err error) {
	value.CurrentVersion, value.Platform = current, runtime.GOOS+"/"+runtime.GOARCH
	if err = ctx.Err(); err != nil {
		return value, err
	}
	if !filepath.IsAbs(directory) {
		return value, ErrManagedInstallRequired
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return value, ErrManagedInstallRequired
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
	var artifact Artifact
	for _, a := range m.Artifacts {
		if a.Platform == value.Platform {
			artifact = a
		}
	}
	temp, err := os.MkdirTemp(filepath.Dir(directory), ".hhc-download-")
	if err != nil {
		return value, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	archive := filepath.Join(temp, "release.archive")
	if err = downloadArtifact(ctx, client, artifact, archive); err != nil {
		return value, err
	}
	if err = bootstrapDownloaded(ctx, directory, m, artifact, archive, selfCheck); err != nil {
		return value, err
	}
	value.TargetVersion, value.SkillVersion, value.Installed = m.Version, m.SkillVersion, true
	return value, nil
}

func bootstrapDownloaded(ctx context.Context, directory string, m Manifest, a Artifact, archive string, check func(context.Context, string, Manifest) error) (err error) {
	if !filepath.IsAbs(directory) || !stableVersion.MatchString(m.Version) {
		return ErrInvalidRelease
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return ErrManagedInstallRequired
	}
	temp, err := os.MkdirTemp(filepath.Dir(directory), ".hhc-install-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	candidate := filepath.Join(temp, ".hhc-prepare-release")
	if err = ExtractBundle(archive, candidate, a); err != nil {
		return err
	}
	if err = check(ctx, candidate, m); err != nil {
		return err
	}
	managed := filepath.Join(temp, ".hhc-prepare-managed")
	if err = os.Mkdir(managed, 0700); err != nil {
		return err
	}
	versions := filepath.Join(managed, "versions")
	if err = os.Mkdir(versions, 0700); err != nil {
		return err
	}
	installed := filepath.Join(versions, m.Version)
	if err = operation.FinalizeDirectory(candidate, installed); err != nil {
		return err
	}
	suffix := ""
	if a.Platform == "windows/amd64" {
		suffix = ".exe"
	}
	from, err := os.Open(filepath.Join(installed, "hhc-launcher"+suffix))
	if err != nil {
		return err
	}
	to, err := os.OpenFile(filepath.Join(managed, "hhc"+suffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		from.Close()
		return err
	}
	_, copyErr := io.CopyBuffer(to, from, make([]byte, 64<<10))
	from.Close()
	syncErr := to.Sync()
	closeErr := to.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return errors.Join(copyErr, syncErr, closeErr)
	}
	if err = writePointer(managed, Pointer{SchemaVersion: 1, Current: m.Version}); err != nil {
		return err
	}
	// A stable, read-only routing skill follows the atomic version pointer.
	// Agent-owned copies of the versioned skill are never modified by updates.
	skill := filepath.Join(managed, "skills", "hhc")
	if err = os.MkdirAll(skill, 0700); err != nil {
		return err
	}
	const loader = "---\nname: hhc\ndescription: Use when operating an installed HHC CLI for church recordings.\n---\n\nRead ../../current.json relative to this skill directory. Read the full\n../../versions/<current>/skills/hhc/SKILL.md for that current version, then follow\nits referenced commands. Do not treat directory listings as the active version.\nUse this skill in place; a copied loader cannot resolve the installation.\n"
	if err = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(loader), 0600); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return operation.FinalizeDirectory(managed, directory)
}
