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
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunToolBoundsOutputAndPreservesArguments(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("supported native OS required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestRunToolChild$", "--", "echo", "聚會 source with spaces"}
	out, err := RunTool(ctx, binary, args, 1024)
	if err != nil || string(out) != "聚會 source with spaces" {
		t.Fatalf("arguments/output: %q %v", out, err)
	}
	args[len(args)-1] = strings.Repeat("x", 1025)
	if out, err := RunTool(ctx, binary, args, 1024); !errors.Is(err, ErrProcessOutputLimit) || out != nil {
		t.Fatalf("unbounded output or false success: %d %v", len(out), err)
	}
	if _, err := RunTool(ctx, "relative.exe", nil, 1024); !errors.Is(err, os.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := RunTool(ctx, binary, []string{"-test.run=^TestRunToolChild$", "--", "echo", string([]byte{255})}, 1024); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("invalid path bytes were normalized: %v", err)
	}
	cancel()
	if _, err := RunTool(ctx, binary, args, 1024); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunToolFailureReportsExitCodeWithoutRawDiagnostics(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native platforms")
	}
	_, err := RunTool(context.Background(), os.Args[0], []string{"-test.run=^TestRunToolChild$", "--", "fail", "sensitive-fixture"}, 1024)
	var failure *ProcessFailure
	if !errors.As(err, &failure) || !errors.Is(err, ErrProcessFailed) || failure.Stage != "tool" || failure.ExitCode != 7 || strings.Contains(err.Error(), "sensitive-fixture") {
		t.Fatalf("safe process failure: %v", err)
	}
}

func TestRunToolMissingWindowsExecutablePreservesSystemCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows CreateProcess diagnostics")
	}
	_, err := RunTool(context.Background(), filepath.Join(t.TempDir(), "sensitive-fixture.exe"), nil, 1024)
	var failure *ProcessFailure
	if !errors.As(err, &failure) || failure.Stage != "create_process" || failure.SystemCode != 2 || strings.Contains(err.Error(), "sensitive-fixture") {
		t.Fatalf("safe native error: %v", err)
	}
}

func TestRunToolCancellationTerminatesDescendants(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("supported native OS required")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunTool(ctx, binary, []string{"-test.run=^TestRunToolChild$", "--", "tree", dir, marker}, 1024)
		done <- err
	}()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("process ended before descendant ready: %v", err)
		case <-ctx.Done():
			t.Fatal("descendant never ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if lock, err := LockWorkspace(dir); !errors.Is(err, ErrOperationBusy) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("descendant did not hold lock: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("false cancellation success: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		lock, err := LockWorkspace(dir)
		if err == nil {
			lock.Close()
			break
		}
		if !errors.Is(err, ErrOperationBusy) || time.Now().After(deadline) {
			t.Fatalf("descendant survived cancellation: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAbruptOwnerExitTerminatesMediaTree(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("supported native OS required")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")
	owner := exec.Command(os.Args[0], "-test.run=^TestRunToolChild$", "--", "owner", dir, marker)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer owner.Process.Kill()
	clean := false
	defer func() {
		if !clean {
			data, _ := os.ReadFile(marker)
			for _, raw := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(raw)
				if err == nil && pid > 1 {
					p, err := os.FindProcess(pid)
					if err == nil {
						p.Kill()
					}
				}
			}
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("media tree did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	deadline = time.Now().Add(3 * time.Second)
	for {
		lock, err := LockWorkspace(dir)
		if err == nil {
			lock.Close()
			clean = true
			break
		}
		if !errors.Is(err, ErrOperationBusy) || time.Now().After(deadline) {
			t.Fatalf("orphaned media after owner exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunToolChild(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) < 2 {
		return
	}
	switch args[0] {
	case "fail":
		fmt.Fprint(os.Stderr, args[1])
		os.Exit(7)
	case "owner":
		_, err := RunTool(context.Background(), os.Args[0], []string{"-test.run=^TestRunToolChild$", "--", "tree", args[1], args[2]}, 1024)
		if err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	case "echo":
		fmt.Print(args[1])
		os.Exit(0)
	case "lock":
		lock, err := LockWorkspace(args[1])
		if err != nil {
			os.Exit(2)
		}
		defer lock.Close()
		fmt.Println("locked")
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=^TestRunToolChild$", "--", "lock", args[1])
		pipe, err := child.StdoutPipe()
		if err != nil {
			os.Exit(3)
		}
		if child.Start() != nil {
			os.Exit(4)
		}
		line, err := bufio.NewReader(pipe).ReadString('\n')
		if err != nil || line != "locked\n" {
			os.Exit(5)
		}
		if os.WriteFile(args[2], []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0600) != nil {
			os.Exit(6)
		}
	default:
		os.Exit(7)
	}
	time.Sleep(time.Minute)
	os.Exit(8)
}
