package operation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeDirectoryNeverReplacesExistingOutput(t *testing.T) {
	parent := t.TempDir()
	staging, err := os.MkdirTemp(parent, ".hhc-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "package.json"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "result")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeDirectory(staging, output); err == nil {
		t.Fatal("replaced existing user directory")
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeDirectory(staging, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "package.json"))
	if err != nil || string(data) != "fixture" {
		t.Fatal("lost finalized bytes")
	}
	if _, err := AvailableBytes(parent); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeOwnedStagingAcrossParentsOnSameVolume(t *testing.T) {
	parent := t.TempDir()
	workspace, err := os.MkdirTemp(parent, ".hhc-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.MkdirTemp(workspace, ".hhc-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "user-package")
	if err := FinalizeDirectory(staging, output); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatal("output missing")
	}
}
