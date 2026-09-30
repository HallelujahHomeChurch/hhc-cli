package operation

import (
	"context"
	"os"
	"syscall"
)

func openStableSource(ctx context.Context, source, _ string, before os.FileInfo) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return nil, err
	}
	// FILE_SHARE_READ allows FFmpeg/probe reads but denies existing or future
	// write/delete handles. A source still held for writing fails closed.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), source)
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() || before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
		f.Close()
		return nil, ErrSourceChanged
	}
	if err := ctx.Err(); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
