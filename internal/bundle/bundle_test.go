package bundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundleRequiresEmbeddedManifestAndExactInstalledBytes(t *testing.T) {
	root := t.TempDir()
	content := []byte("non-executable verification fixture")
	sum := sha256.Sum256(content)
	file := File{SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(content))}
	m := Manifest{SchemaVersion: 1, BundleVersion: "ffmpeg-8.1.2-hhc.1", Platform: runtime.GOOS + "/" + runtime.GOARCH, FFmpeg: file, FFprobe: file}
	data, _ := json.Marshal(m)
	encoded := base64.StdEncoding.EncodeToString(data)
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(root, name), content, 0700); err != nil {
			t.Fatal(err)
		}
	}
	tools, err := verifyAt(root, encoded)
	if err != nil || tools.Version != m.BundleVersion || !filepath.IsAbs(tools.FFmpeg) || !filepath.IsAbs(tools.FFprobe) {
		t.Fatalf("valid bundle: %+v %v", tools, err)
	}
	if _, err := verifyAt(root, ""); err == nil {
		t.Fatal("trusted missing manifest")
	}
	m.Platform = "unsupported/fixture"
	wrong, _ := json.Marshal(m)
	if _, err := verifyAt(root, base64.StdEncoding.EncodeToString(wrong)); err == nil {
		t.Fatal("trusted another platform")
	}
	if err := os.WriteFile(tools.FFmpeg, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAt(root, encoded); err == nil {
		t.Fatal("trusted changed executable")
	}
}
