//go:build !darwin && !linux

package operation

func superviseMedia() int { return 1 }
