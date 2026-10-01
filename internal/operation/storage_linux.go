package operation

import "golang.org/x/sys/unix"

func finalizeDirectory(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
func availableBytes(directory string) (uint64, error) {
	var value unix.Statfs_t
	if err := unix.Statfs(directory, &value); err != nil {
		return 0, err
	}
	return value.Bavail * uint64(value.Bsize), nil
}
