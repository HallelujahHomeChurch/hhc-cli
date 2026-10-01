package operation

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if handled, status := HandleSupervisor(os.Args[1:]); handled {
		os.Exit(status)
	}
	os.Exit(m.Run())
}
