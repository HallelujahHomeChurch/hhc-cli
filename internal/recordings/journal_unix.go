//go:build !windows

package recordings

import "os"

func replaceJournal(root *os.Root, _ string, temp string) error {
	if err := root.Rename(temp, "journal.json"); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
