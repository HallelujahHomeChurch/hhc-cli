package operation

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

func TestWorkspaceLockRecoversAfterProcessDeath(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("supported native OS required")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceLockChild$")
	child.Env = append(os.Environ(), "HHC_TEST_LOCK_DIR="+dir)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(output).ReadString('\n')
		if err == nil && line != "locked\n" {
			err = fmt.Errorf("unexpected child marker %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("child did not acquire native lock")
	}
	if f, err := LockWorkspace(dir); !errors.Is(err, ErrOperationBusy) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("ignored live process lock: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	recovered, err := LockWorkspace(dir)
	if err != nil {
		t.Fatalf("stale lock after crash: %v", err)
	}
	recovered.Close()
}

func TestWorkspaceLockChild(t *testing.T) {
	dir := os.Getenv("HHC_TEST_LOCK_DIR")
	if dir == "" {
		t.Skip("subprocess fixture")
	}
	lock, err := LockWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Println("locked")
	// The parent kills this process; no graceful unlock is involved.
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	<-timer.C
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
