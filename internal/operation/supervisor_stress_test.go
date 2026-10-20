//go:build darwin || linux

package operation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestSupervisorFastToolExit(t *testing.T) {
	for i := 0; i < 100; i++ {
		if _, err := RunTool(context.Background(), "/usr/bin/true", nil, 1024); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
}

func TestSupervisorRejectsCallerProcessGroup(t *testing.T) {
	cmd := exec.Command(os.Args[0], supervisorFlag)
	err := cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 71 {
		t.Fatalf("caller process group guard: %v", err)
	}
}
