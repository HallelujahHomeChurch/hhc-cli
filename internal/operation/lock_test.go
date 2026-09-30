package operation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceLockIsExclusiveAndReusableAfterClose(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("supported native OS required")
	}
	dir := t.TempDir()
	first, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := LockWorkspace(dir); !errors.Is(err, ErrOperationBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("concurrent workspace accepted: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	if _, err := os.Lstat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatal("lock file must survive close to avoid inode races")
	}
}

func TestWorkspaceLockRejectsRelativeDirectoryAndSymlink(t *testing.T) {
	if f, err := LockWorkspace("relative"); err == nil {
		f.Close()
		t.Fatal("relative workspace accepted")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("symlink test on macOS")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "user-file")
	if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if f, err := LockWorkspace(dir); err == nil {
		f.Close()
		t.Fatal("symlink lock accepted")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "preserve" {
		t.Fatal("modified user file")
	}
}
