package recordings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func TestStandalonePrepareRecoversFinalizedOutputWithoutSourceOrBundle(t *testing.T) {
	parent := t.TempDir()
	intent := Intent{Command: "prepare", Input: filepath.Join(parent, "source-no-longer-present.mp4"), Output: filepath.Join(parent, "keep-package"), VideoBitrate720: 1500000, VideoBitrate1080: 3000000}
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := os.MkdirAll(filepath.Join(intent.Output, "720p"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"master.m3u8", "720p/index.m3u8", "720p/init.mp4", "720p/seg-000000.m4s"} {
		if err := os.WriteFile(filepath.Join(intent.Output, filepath.FromSlash(path)), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := media.BuildPackageInventory(context.Background(), intent.Output, []media.RecordingRendition{{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 5, SegmentCount: 1}}, "cpu-hq-v1")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(inv)
	if err := os.WriteFile(filepath.Join(intent.Output, "package.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), j); err == nil {
		t.Fatal("adopted an existing user output before checkpoint")
	}
	if _, err := j.generatedWorkspace(); err != nil {
		t.Fatal(err)
	}
	state := j.State()
	state.PackageDigest = inv.InventoryDigest
	state.PackageBytes, _ = media.ValidateRecordingInventory(inv)
	state.SourceFingerprint = media.SourceFingerprint{SHA256: strings.Repeat("a", 64), SizeBytes: 123, ModifiedAt: time.Now().UTC()}
	if err := j.Save(state); err != nil {
		t.Fatal(err)
	}
	value, err := Prepare(context.Background(), j)
	if err != nil || !value.RequestedActionSatisfied || value.PackageDigest != inv.InventoryDigest || value.OutputPath != intent.Output {
		t.Fatalf("recovery %+v %v", value, err)
	}
	if _, err := os.Stat(filepath.Join(intent.Output, "package.json")); err != nil {
		t.Fatal("deleted user output")
	}
}
