package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
)

// This exercises the real command and embedded hash verification with explicit
// test-only tools. It is not acceptance of a distributable FFmpeg build.
func TestNativeStandalonePrepareWithVerifiedFixtureBundle(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("supported native preparation platforms")
	}
	ffmpeg, ffprobe := os.Getenv("HHC_TEST_FFMPEG"), os.Getenv("HHC_TEST_FFPROBE")
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("HHC_REQUIRE_MEDIA_TESTS") == "1" {
			t.Fatal("explicit tools required")
		}
		t.Skip("explicit native media tools required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	parent := t.TempDir()
	toolDirectory := filepath.Join(parent, "ffmpeg")
	if err := os.Mkdir(toolDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest{SchemaVersion: 1, BundleVersion: "native-fixture-v1", Platform: runtime.GOOS + "/" + runtime.GOARCH}
	for _, tool := range []struct {
		name, path string
		target     *bundle.File
	}{{"ffmpeg", ffmpeg, &manifest.FFmpeg}, {"ffprobe", ffprobe, &manifest.FFprobe}} {
		name := tool.name
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		source, err := os.Open(tool.path)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(filepath.Join(toolDirectory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		hash := sha256.New()
		size, err := io.Copy(io.MultiWriter(out, hash), source)
		source.Close()
		closeErr := out.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("fixture copy: %v %v", err, closeErr)
		}
		*tool.target = bundle.File{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size}
	}
	encoded, _ := json.Marshal(manifest)
	binary := filepath.Join(parent, "hhc")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X github.com/HallelujahHomeChurch/hhc-cli/internal/bundle.manifestBase64="+base64.StdEncoding.EncodeToString(encoded), "-o", binary, "./cmd/hhc")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	source := filepath.Join(parent, "聚會 原始.mp4")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=blue:s=1280x720:r=2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "35", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", source).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, out)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(original)
	config, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		config, err = os.UserCacheDir()
	}
	if err != nil {
		t.Fatal(err)
	}
	// A fixed UUID is safe because each native runner is isolated; an existing
	// directory is a collision, never permission to delete somebody else's data.
	id := "00000000-0000-4000-8000-000000009907"
	journal := filepath.Join(config, "HHC", "cli", "operations", id)
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("fixture operation collision")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(journal); err != nil {
			t.Error(err)
		}
	})
	output := filepath.Join(parent, "保留 HLS")
	for _, args := range [][]string{
		{"recordings", "prepare", source, "--output", output, "--operation-id", id, "--json", "--no-input"},
		{"recordings", "resume", id, "--json", "--no-input"},
	} {
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("command: %v %s", err, out)
		}
		var value struct {
			OK        bool
			Profile   any
			Principal any
			Data      struct {
				RequestedActionSatisfied        bool
				PrepareState, LocalCleanupState string
			}
		}
		if json.Unmarshal(out, &value) != nil || !value.OK || value.Profile != nil || value.Principal != nil || !value.Data.RequestedActionSatisfied || value.Data.PrepareState != "complete" || value.Data.LocalCleanupState != "complete" {
			t.Fatalf("command result: %s", out)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil || sha256.Sum256(after) != hash {
		t.Fatal("changed source")
	}
	if _, err := os.Stat(filepath.Join(output, "package.json")); err != nil {
		t.Fatal("user output missing")
	}
	if _, err := os.Stat(filepath.Join(parent, ".hhc-prepare-"+id)); !os.IsNotExist(err) {
		t.Fatal("retained owned workspace")
	}
}
