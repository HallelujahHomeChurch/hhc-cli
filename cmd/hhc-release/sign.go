package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

const mediaBundleVersion = "ffmpeg-8.1.3-x264-b35605a-hhc2"

func signRelease(directory, version, encodedSeed, publicKey string) error {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedSeed))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("invalid signing configuration")
	}
	defer clear(seed)
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	pub, err := hex.DecodeString(publicKey)
	if err != nil || !private.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) {
		return errors.New("signing key does not match embedded trust")
	}
	m := update.Manifest{SchemaVersion: 1, Version: version, JournalSchema: 1, SkillVersion: version, BundleVersion: mediaBundleVersion}
	var sums strings.Builder
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		name, err := artifactName(version, platform)
		if err != nil {
			return err
		}
		a, err := artifactMetadata(filepath.Join(directory, name), version, platform)
		if err != nil {
			return err
		}
		m.Artifacts = append(m.Artifacts, a)
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, name)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	sig := ed25519.Sign(private, data)
	if _, err := update.VerifyManifest(data, sig, ed25519.PublicKey(pub), "windows/amd64", 1); err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"release.json", data}, {"release.json.sig", sig}} {
		if err := writeNew(filepath.Join(directory, file.name), file.data, 0600); err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(file.data), file.name)
	}
	return writeNew(filepath.Join(directory, "SHA256SUMS"), []byte(sums.String()), 0600)
}

func writeNew(path string, data []byte, mode os.FileMode) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
