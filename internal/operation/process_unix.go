//go:build darwin || linux

package operation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func runTool(ctx context.Context, binary string, args []string, stdout io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(supervisorRequest{Binary: binary, Args: args})
	if err != nil || len(payload) > 65536 {
		return ErrProcessFailed
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer controlRead.Close()
	defer controlWrite.Close()
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inputRead.Close()
	defer inputWrite.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer statusRead.Close()
	defer statusWrite.Close()
	cmd := exec.Command(executable, supervisorFlag)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{controlRead, inputRead, statusWrite}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	controlRead.Close()
	inputRead.Close()
	statusWrite.Close()
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
	if _, err := inputWrite.Write(payload); err != nil {
		unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		cmd.Wait()
		close(done)
		<-stopped
		return ErrProcessFailed
	}
	inputWrite.Close()
	err = cmd.Wait()
	// A completed tool may not leave grandchildren holding pipes or locks.
	_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	close(done)
	<-stopped
	status, readErr := io.ReadAll(io.LimitReader(statusRead, 2))
	var exited *exec.ExitError
	if readErr == nil && len(status) == 1 && status[0] == 0 && errors.As(err, &exited) {
		if wait, ok := exited.Sys().(syscall.WaitStatus); ok && wait.Signaled() && wait.Signal() == syscall.SIGKILL {
			return nil
		}
	}
	return ErrProcessFailed
}
