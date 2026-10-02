package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCleanupDownloadWindowsSharingViolation(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(map[bool]string{true: "transient", false: "persistent"}[release], func(t *testing.T) {
			directory := t.TempDir()
			archive := filepath.Join(directory, "release.archive")
			if err := os.WriteFile(archive, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			path, err := windows.UTF16PtrFromString(archive)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if release {
				done := make(chan struct{})
				go func() { time.Sleep(150 * time.Millisecond); windows.CloseHandle(handle); close(done) }()
				err = cleanupDownload(directory)
				<-done
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatalf("temporary directory remains: %v", err)
				}
			} else {
				err = cleanupDownload(directory)
				windows.CloseHandle(handle)
				var failure *CleanupFailure
				if !errors.As(err, &failure) || failure.SystemCode != 32 {
					t.Fatalf("lock failure: %v", err)
				}
				if data, err := os.ReadFile(archive); err != nil || string(data) != "fixture" {
					t.Fatal("changed locked archive")
				}
			}
		})
	}
}
