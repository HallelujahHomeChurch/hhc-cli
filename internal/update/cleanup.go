package update

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

// CleanupFailure excludes paths and raw OS messages from public diagnostics.
type CleanupFailure struct{ SystemCode uint64 }

func (e *CleanupFailure) Error() string {
	return fmt.Sprintf("update_cleanup_failed (os=%d)", e.SystemCode)
}

// Only the freshly created download directory is passed here. Retry transient
// Windows sharing/lock violations, never alter permissions or stop other apps.
func cleanupDownload(directory string) error {
	for attempt := 0; ; attempt++ {
		err := os.RemoveAll(directory)
		if err == nil {
			return nil
		}
		var code syscall.Errno
		errors.As(err, &code)
		if runtime.GOOS != "windows" || (code != 32 && code != 33) || attempt == 4 {
			return &CleanupFailure{SystemCode: uint64(code)}
		}
		time.Sleep((100 * time.Millisecond) << attempt)
	}
}
