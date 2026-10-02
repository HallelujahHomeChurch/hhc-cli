package operation

import (
	"context"
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runTool(ctx context.Context, binary string, args []string, stdout io.Writer) (err error) {
	stage := "create_job"
	defer func() {
		if err != nil && !errors.Is(err, ErrProcessFailed) && ctx.Err() == nil {
			err = processFailure(stage, err)
		}
	}()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	stage = "configure_job"
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}

	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var read, write windows.Handle
	stage = "create_pipe"
	if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return err
	}
	reader := os.NewFile(uintptr(read), "media-stdout")
	defer reader.Close()
	defer func() {
		if write != 0 {
			_ = windows.CloseHandle(write)
		}
	}()
	stage = "configure_pipe"
	if err := windows.SetHandleInformation(read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return err
	}
	nul, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return err
	}
	stage = "open_stdio"
	input, err := windows.CreateFile(nul, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(input)
	stage = "create_attributes"
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return err
	}
	defer attributes.Delete()
	// Windows 10+ job-list assignment is atomic with process creation. A
	// suspended-then-assigned process can be orphaned if the CLI dies between.
	const jobListAttribute = 0x0002000d
	stage = "assign_job"
	if err := attributes.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return err
	}
	handles := []windows.Handle{write, input}
	stage = "assign_handles"
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags, si.StdOutput, si.StdErr, si.StdInput = windows.STARTF_USESTDHANDLES, write, input, input
	application, err := windows.UTF16PtrFromString(binary)
	if err != nil {
		return err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{binary}, args...)))
	if err != nil {
		return err
	}
	var process windows.ProcessInformation
	if err := ctx.Err(); err != nil {
		return err
	}
	stage = "create_process"
	if err := windows.CreateProcess(application, command, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_NO_WINDOW, nil, nil, &si.StartupInfo, &process); err != nil {
		return err
	}
	defer windows.CloseHandle(process.Process)
	windows.CloseHandle(process.Thread)
	// Only the child retains the writing end; EOF follows job termination.
	windows.CloseHandle(write)
	write = 0
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(stdout, reader); copied <- err }()
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = windows.TerminateJobObject(job, 1)
		case <-done:
		}
	}()
	_, waitErr := windows.WaitForSingleObject(process.Process, windows.INFINITE)
	var exit uint32
	exitErr := windows.GetExitCodeProcess(process.Process, &exit)
	terminateErr := windows.TerminateJobObject(job, 1)
	close(done)
	<-stopped
	copyErr := <-copied
	for _, failure := range []struct {
		stage string
		err   error
	}{
		{"wait_process", waitErr}, {"read_exit", exitErr}, {"terminate_job", terminateErr}, {"read_stdout", copyErr},
	} {
		if failure.err != nil {
			return processFailure(failure.stage, failure.err)
		}
	}
	if exit != 0 {
		return &ProcessFailure{Stage: "tool", ExitCode: int(exit)}
	}
	return nil
}
