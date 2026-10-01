//go:build !windows && !darwin && !linux

package operation

import "errors"

func finalizeDirectory(string, string) error { return errors.New("unsupported_platform") }
func availableBytes(string) (uint64, error)  { return 0, errors.New("unsupported_platform") }
