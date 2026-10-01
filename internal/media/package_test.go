package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackageInventoryHashesActualFilesWithoutChangingThem(t *testing.T) {
	dir := t.TempDir()
	r := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 65, SegmentCount: 3}
	if err := os.Mkdir(filepath.Join(dir, "720p"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{"master.m3u8", "720p/index.m3u8", "720p/init.mp4", "720p/seg-000000.m4s", "720p/seg-000001.m4s", "720p/seg-000002.m4s"}
	for _, path := range paths {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := BuildPackageInventory(context.Background(), dir, []RecordingRendition{r}, "hls-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Objects) != 6 || inv.InventoryDigest == "" {
		t.Fatalf("incomplete inventory: %+v", inv)
	}
	for _, object := range inv.Objects {
		expected := sha256.Sum256([]byte(object.Path))
		if object.SHA256 != hex.EncodeToString(expected[:]) || object.SizeBytes != int64(len(object.Path)) {
			t.Fatalf("not measured from actual bytes: %+v", object)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("builder changed the directory: %v", err)
	}
	t.Run("read package validates current bytes", func(t *testing.T) {
		manifest, _ := json.Marshal(inv)
		path := filepath.Join(dir, "package.json")
		if err := os.WriteFile(path, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		read, err := ReadPackage(context.Background(), dir)
		if err != nil || read.InventoryDigest != inv.InventoryDigest {
			t.Fatalf("read: %v", err)
		}
		object := filepath.Join(dir, "master.m3u8")
		if err := os.WriteFile(object, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(object, []byte("master.m3u8"), 0600)
		if _, err := ReadPackage(context.Background(), dir); err == nil {
			t.Fatal("accepted changed package")
		}
	})
	for _, failure := range []string{"extra file", "missing segment", "symlink", "oversized", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(dir, "720p", "seg-000002.m4s")
			switch failure {
			case "extra file":
				path = filepath.Join(dir, "unexpected.txt")
				if err := os.WriteFile(path, []byte("not a package object"), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
			case "missing segment":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(path, []byte("720p/seg-000002.m4s"), 0600)
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "master.m3u8"), path); err != nil {
					t.Fatal(err)
				}
				defer func() { os.Remove(path); os.WriteFile(path, []byte("720p/seg-000002.m4s"), 0600) }()
			case "oversized":
				if err := os.Truncate(path, RecordingObjectMaxBytes+1); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(path, []byte("720p/seg-000002.m4s"), 0600)
			case "cancelled":
				cancel()
			}
			result, err := BuildPackageInventory(ctx, dir, []RecordingRendition{r}, "hls-v1")
			if err == nil || result.InventoryDigest != "" {
				t.Fatalf("accepted %s: %+v", failure, result)
			}
			if failure == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
