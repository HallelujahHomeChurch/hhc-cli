package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func installedFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "versions"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Pointer{SchemaVersion: 1, Current: "1.0.0"})
	if err := os.WriteFile(filepath.Join(root, "current.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestInstallSwitchAndFailurePreserveCurrent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := installedFixture(t)
		m, _, _ := releaseFixture(t)
		archive, a := archiveFixture(t, "darwin/arm64", validEntries("darwin/arm64"))
		m.Artifacts[1] = a
		checked := false
		err := installDownloaded(context.Background(), root, m, a, archive, func(ctx context.Context, directory string, manifest Manifest) error {
			checked = true
			if _, err := os.Stat(filepath.Join(directory, "ffmpeg", "ffprobe")); err != nil {
				t.Fatal(err)
			}
			if fail {
				return ErrInvalidRelease
			}
			return nil
		})
		if !checked || (err != nil) != fail {
			t.Fatalf("install fail=%v: %v", fail, err)
		}
		p, err := ReadPointer(root)
		if err != nil {
			t.Fatal(err)
		}
		if fail {
			if p.Current != "1.0.0" {
				t.Fatal("failed update replaced current")
			}
		} else if p.Current != "1.2.3" || p.Previous != "1.0.0" {
			t.Fatalf("pointer %+v", p)
		}
	}
}
func TestBusyInstallDoesNotExtract(t *testing.T) {
	root := installedFixture(t)
	lock, err := operation.LockSharedWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	m, _, _ := releaseFixture(t)
	err = installDownloaded(context.Background(), root, m, m.Artifacts[0], "nonexistent", func(context.Context, string, Manifest) error { t.Fatal("executed while busy"); return nil })
	if !errors.Is(err, operation.ErrOperationBusy) {
		t.Fatalf("busy: %v", err)
	}
	p, _ := ReadPointer(root)
	if p.Current != "1.0.0" {
		t.Fatal("changed current while busy")
	}
}

func TestInterruptedSwitchReusesOnlyIdenticalAuthenticatedVersion(t *testing.T) {
	for _, changed := range []bool{false, true} {
		root := installedFixture(t)
		m, _, _ := releaseFixture(t)
		archive, a := archiveFixture(t, "darwin/arm64", validEntries("darwin/arm64"))
		target := filepath.Join(root, "versions", m.Version)
		if err := ExtractBundle(archive, target, a); err != nil {
			t.Fatal(err)
		}
		if changed {
			if err := os.WriteFile(filepath.Join(target, "unknown-user-file"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		err := installDownloaded(context.Background(), root, m, a, archive, func(context.Context, string, Manifest) error { return nil })
		if (err != nil) != changed {
			t.Fatalf("changed=%v: %v", changed, err)
		}
		p, err := ReadPointer(root)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			if p.Current != "1.0.0" {
				t.Fatal("selected modified version")
			}
			if data, err := os.ReadFile(filepath.Join(target, "unknown-user-file")); err != nil || string(data) != "preserve" {
				t.Fatal("modified unknown target")
			}
		} else if p.Current != m.Version {
			t.Fatal("did not recover interrupted switch")
		}
	}
}
