//go:build !windows && !darwin

package operation

import "os"

func lockWorkspace(*os.File) error { return os.ErrInvalid }
