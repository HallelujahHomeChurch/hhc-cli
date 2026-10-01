//go:build !windows && !darwin && !linux

package operation

import "os"

func lockWorkspace(*os.File) error { return os.ErrInvalid }
