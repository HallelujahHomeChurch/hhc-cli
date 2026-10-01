package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapCreatesOnlyNewManagedInstallation(t *testing.T) {
	m, _, _ := releaseFixture(t)
	archive, a := archiveFixture(t, "darwin/arm64", validEntries("darwin/arm64"))
	directory := filepath.Join(t.TempDir(), "hhc")
	check := func(context.Context, string, Manifest) error { return nil }
	if err := bootstrapDownloaded(context.Background(), directory, m, a, archive, check); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPointer(directory)
	if err != nil || p.Current != m.Version {
		t.Fatalf("bootstrap pointer: %+v %v", p, err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "hhc")); err != nil || string(data) != "launcher" {
		t.Fatal("missing stable launcher")
	}
	if _, err := os.Stat(filepath.Join(directory, "skills", "hhc", "SKILL.md")); err != nil {
		t.Fatal("missing stable skill reference")
	}
	if err := bootstrapDownloaded(context.Background(), directory, m, a, archive, check); err == nil {
		t.Fatal("overwrote installation")
	}
	if data, err := os.ReadFile(filepath.Join(directory, "hhc")); err != nil || string(data) != "launcher" {
		t.Fatal("changed existing installation")
	}
	failed := filepath.Join(t.TempDir(), "failed")
	if err := bootstrapDownloaded(context.Background(), failed, m, a, archive, func(context.Context, string, Manifest) error { return ErrInvalidRelease }); err == nil {
		t.Fatal("accepted failed self check")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("published partial install")
	}
}
