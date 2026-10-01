package recordings

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceJournal(_ *os.Root, directory, temp string) error {
	from, err := windows.UTF16PtrFromString(filepath.Join(directory, temp))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(directory, "journal.json"))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
