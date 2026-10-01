//go:build darwin || linux

package operation

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func superviseMedia() int {
	// Never signal a caller's terminal/process group when invoked manually.
	if unix.Getpgrp() != os.Getpid() {
		return 1
	}
	control, input, status := os.NewFile(3, "owner-lifetime"), os.NewFile(4, "tool-request"), os.NewFile(5, "tool-status")
	for _, file := range []*os.File{control, input, status} {
		if file == nil {
			return 1
		}
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return 1
		}
		unix.CloseOnExec(int(file.Fd())) // Media descendants must not inherit control/status capabilities.
	}
	defer control.Close()
	defer input.Close()
	defer status.Close()
	var request supervisorRequest
	data, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, &request) != nil || !filepath.IsAbs(request.Binary) || strings.ContainsRune(request.Binary, 0) {
		return 1
	}
	for _, arg := range request.Args {
		if strings.ContainsRune(arg, 0) {
			return 1
		}
	}
	// The owner alone holds the pipe's writing end. EOF also covers SIGKILL,
	// panic and abrupt terminal exit, when the owner's goroutines cannot run.
	go func() { io.Copy(io.Discard, control); unix.Kill(-os.Getpid(), unix.SIGKILL) }()
	cmd := exec.Command(request.Binary, request.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	result := byte(0)
	if err := cmd.Run(); err != nil {
		result = 1
	}
	status.Write([]byte{result})
	// Killing our own dedicated group also reaps media grandchildren. The
	// parent accepts this deliberate signal only with the separate status byte.
	unix.Kill(-os.Getpid(), unix.SIGKILL)
	return 1
}
