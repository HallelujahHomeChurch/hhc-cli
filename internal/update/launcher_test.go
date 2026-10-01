package update

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestLauncherPreservesArgumentsAndExit(t *testing.T) {
	root := installedFixture(t)
	version := filepath.Join(root, "versions", "1.0.0")
	if err := os.Mkdir(version, 0700); err != nil {
		t.Fatal(err)
	}
	name := "hhc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := filepath.Join(t.TempDir(), "main.go")
	// A separately compiled harmless fixture exercises native argv and exit-code
	// forwarding, including characters that would execute inside a shell.
	if err := os.WriteFile(source, []byte("package main\nimport (\"fmt\";\"os\")\nfunc main(){fmt.Print(os.Args[1]);os.Exit(7)}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(version, name), source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	var out bytes.Buffer
	exit, err := Launch(root, []string{"主日 $(whoami)"}, nil, &out, &out)
	if err != nil || exit != 7 || out.String() != "主日 $(whoami)" {
		t.Fatalf("launch: %d %v %q", exit, err, out.String())
	}
	lock, err := operation.LockWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Launch(root, nil, nil, &out, &out); !errors.Is(err, operation.ErrOperationBusy) {
		t.Fatalf("busy launcher: %v", err)
	}
}
