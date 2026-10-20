package operation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsLockRejectsReparseLeaf(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".lock")); err != nil {
		t.Skipf("native symlink privilege unavailable: %v", err)
	}
	if f, err := LockWorkspace(dir); err == nil {
		f.Close()
		t.Fatal("reparse leaf accepted")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "preserve" {
		t.Fatal("original changed")
	}
}

func TestWindowsLockCannotBeReplacedWhileHeld(t *testing.T) {
	dir := t.TempDir()
	f, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(filepath.Join(dir, ".lock"), filepath.Join(dir, "moved")); err == nil {
		t.Fatal("held lock inode was replaceable")
	}
}

func TestWindowsLockRejectsDirectoryLeaf(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if f, err := LockWorkspace(dir); err == nil {
		f.Close()
		t.Fatal("directory lock accepted")
	}
}

func TestWindowsLockRejectsReparseDirectory(t *testing.T) {
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("native symlink privilege unavailable: %v", err)
	}
	if f, err := LockWorkspace(link); err == nil {
		f.Close()
		t.Fatal("reparse directory accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, ".lock")); !os.IsNotExist(err) {
		t.Fatal("lock created outside requested directory")
	}
}
