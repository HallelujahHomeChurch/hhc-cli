package operation

import "golang.org/x/sys/unix"

func finalizeDirectory(from, to string) error { return unix.RenamexNp(from, to, unix.RENAME_EXCL) }
func availableBytes(directory string) (uint64, error) {
	var value unix.Statfs_t
	if err := unix.Statfs(directory, &value); err != nil {
		return 0, err
	}
	return value.Bavail * uint64(value.Bsize), nil
}
