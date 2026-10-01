package media

import (
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if handled, status := operation.HandleSupervisor(os.Args[1:]); handled {
		os.Exit(status)
	}
	os.Exit(m.Run())
}
