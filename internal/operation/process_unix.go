//go:build darwin || linux

package operation

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func runTool(ctx context.Context, binary string, args []string, stdout io.Writer) error {
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		case <-done:
		}
	}()
	err := cmd.Wait()
	// A completed tool may not leave grandchildren holding pipes or locks.
	_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	close(done)
	<-stopped
	return err
}
