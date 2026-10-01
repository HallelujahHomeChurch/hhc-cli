//go:build darwin || linux

package operation

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockWorkspace(f *os.File, shared bool) error {
	mode := unix.LOCK_EX
	if shared {
		mode = unix.LOCK_SH
	}
	err := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrOperationBusy
	}
	return err
}
