package auth

import (
	"context"
	"golang.org/x/sys/windows"
	"runtime"
	"syscall"
	"unsafe"
)

func openLoginBrowser(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil && err != syscall.Errno(windows.S_FALSE) {
		return ErrAuthUnavailable
	}
	defer windows.CoUninitialize()
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// ShellExecute reports failure in its return value, not necessarily in
	// GetLastError. Treat every value <=32 as failure, even if last-error is 0.
	result, _, _ := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, uintptr(windows.SW_SHOWNORMAL))
	if result <= 32 {
		return ErrAuthUnavailable
	}
	return nil
}
