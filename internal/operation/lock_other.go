//go:build !windows && !darwin && !linux

package operation

import "os"

func lockWorkspace(*os.File, bool) error { return os.ErrInvalid }
