package build

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedSourceFallbackFailsClosed(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("recipe requires Bash")
	}
	recipe, err := os.ReadFile("ffmpeg.sh")
	if err != nil {
		t.Fatal(err)
	}
	start, end := strings.Index(string(recipe), "fetch()"), strings.Index(string(recipe), "\nfetch sources/ffmpeg")
	if start < 0 || end < start {
		t.Fatal("source functions missing")
	}
	functions := string(recipe[start:end])
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned fixture")))
	for _, tc := range []struct {
		name, primary, fallback string
		calls                   int
		success                 bool
	}{
		{"primary verified", "pinned fixture", "unused", 1, true},
		{"primary challenge fallback verified", "<html>challenge</html>", "pinned fixture", 2, true},
		{"both invalid", "<html>challenge</html>", "different source", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "source.tar.bz2")
			script := functions + `
calls=0
fetch() {
 calls=$((calls+1))
 case "$2" in
  primary) printf '%s' "$primary_bytes" > "$1" ;;
  fallback) printf '%s' "$fallback_bytes" > "$1" ;;
  *) exit 90 ;;
 esac
}
if fetch_verified "$1" "$2" primary fallback; then result=0; else result=1; fi
test "$calls" = "$3" || exit 91
exit "$result"
`
			command := exec.Command(bash, "-c", script, "source-test", destination, hash, fmt.Sprint(tc.calls))
			command.Env = append(os.Environ(), "primary_bytes="+tc.primary, "fallback_bytes="+tc.fallback)
			output, err := command.CombinedOutput()
			if tc.success && err != nil {
				t.Fatalf("%v %s", err, output)
			}
			if !tc.success {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("invalid bytes not fenced: %v %s", err, output)
				}
			}
			if tc.success {
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "pinned fixture" {
					t.Fatal("accepted unverified source", err)
				}
			}
		})
	}
}
