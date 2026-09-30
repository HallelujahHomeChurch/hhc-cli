package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSourceFingerprintReadsWholeSourceWithoutChangingOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "聚會.mp4")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(2, 0); err != nil {
		t.Fatal(err)
	}
	got, err := FingerprintSource(context.Background(), f)
	if err != nil || got.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || got.SizeBytes != 3 || got.ModifiedAt.IsZero() {
		t.Fatalf("not original bytes: %+v %v", got, err)
	}
	if offset, err := f.Seek(0, 1); err != nil || offset != 2 {
		t.Fatalf("source offset changed: %d %v", offset, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "abc" {
		t.Fatal("original changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := FingerprintSource(ctx, f); !errors.Is(err, context.Canceled) || got.SHA256 != "" {
		t.Fatalf("cancelled success: %+v %v", got, err)
	}
}

func TestSourceFingerprintRejectsOversizeBeforeReading(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sparse fixture; not native Windows capacity acceptance")
	}
	path := filepath.Join(t.TempDir(), "oversize.mp4")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Sparse metadata fixture, not real 50 GB media/capacity acceptance.
	if err := f.Truncate(50_000_000_001); err != nil {
		t.Fatal(err)
	}
	if got, err := FingerprintSource(context.Background(), f); !errors.Is(err, ErrRecordingSourceTooLarge) || got.SHA256 != "" {
		t.Fatalf("oversize source read: %+v %v", got, err)
	}
}
