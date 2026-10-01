package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
)

func TestNativeReleasePackage(t *testing.T) {
	media := os.Getenv("HHC_TEST_RELEASE_BUNDLE")
	if media == "" {
		t.Skip("requires the verified native CI media bundle")
	}
	t.Chdir(filepath.Join("..", ".."))
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := packageRelease(context.Background(), t.TempDir(), media, "0.0.0", hex.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseSignatureMatchesEmbeddedTrust(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		name, err := artifactName("1.2.3", platform)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(platform), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := signRelease(dir, "1.2.3", base64.StdEncoding.EncodeToString(priv.Seed()), hex.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, "release.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := update.VerifyManifest(data, sig, pub, "darwin/arm64", 1); err != nil {
		t.Fatal(err)
	}
	if err := signRelease(dir, "1.2.3", base64.StdEncoding.EncodeToString(priv.Seed()), hex.EncodeToString(pub)); err == nil {
		t.Fatal("overwrote signed release")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := signRelease(t.TempDir(), "1.2.3", base64.StdEncoding.EncodeToString(priv.Seed()), hex.EncodeToString(other)); err == nil {
		t.Fatal("accepted mismatched public key")
	}
}

func TestArchiveRoundTripWithUpdater(t *testing.T) {
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		root := t.TempDir()
		suffix := ""
		if platform == "windows/amd64" {
			suffix = ".exe"
		}
		for _, path := range []string{"hhc" + suffix, "hhc-launcher" + suffix, "ffmpeg/ffmpeg" + suffix, "ffmpeg/ffprobe" + suffix, "skills/hhc/SKILL.md", "skills/hhc/references/commands.md", "licenses/Go-LICENSE.txt", "source/ffmpeg.tar.xz"} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, path), []byte(path), 0700); err != nil {
				t.Fatal(err)
			}
		}
		out := t.TempDir()
		path, err := archiveRelease(root, out, "1.2.3", platform)
		if err != nil {
			t.Fatal(err)
		}
		a, err := artifactMetadata(path, "1.2.3", platform)
		if err != nil {
			t.Fatal(err)
		}
		if err := update.ExtractBundle(path, filepath.Join(t.TempDir(), "verify"), a); err != nil {
			t.Fatal(err)
		}
		if _, err := archiveRelease(root, out, "1.2.3", platform); err == nil {
			t.Fatal("overwrote release archive")
		}
	}
}
