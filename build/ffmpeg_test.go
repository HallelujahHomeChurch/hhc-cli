package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func recipeFunctions(t *testing.T) string {
	t.Helper()
	recipe, err := os.ReadFile("ffmpeg.sh")
	if err != nil {
		t.Fatal(err)
	}
	start, end := strings.Index(string(recipe), "fetch()"), strings.Index(string(recipe), "\nfetch sources/ffmpeg")
	if start < 0 || end < start {
		t.Fatal("source functions missing")
	}
	return string(recipe[start:end])
}

func TestPinnedSourceFallbackFailsClosed(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("recipe requires Bash")
	}
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
			script := recipeFunctions(t) + `
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

func TestX264RecoveryUsesOnlyVerifiedSource(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("recipe requires Bash")
	}
	for _, tc := range []struct {
		name, member, content                                string
		badOuter, fetchFailure, officialUnavailable, success bool
		officialReadyAt                                      int
	}{
		{name: "primary preferred", member: "source/x264.tar.bz2", content: "pinned source", officialReadyAt: 1, success: true},
		{name: "inline variant preferred", member: "source/x264.tar.bz2", content: "pinned source", officialReadyAt: 2, success: true},
		{name: "valid signed-release source", member: "source/x264.tar.bz2", content: "pinned source", success: true},
		{name: "official sources unavailable", member: "source/x264.tar.bz2", content: "pinned source", officialUnavailable: true, success: true},
		{name: "outer mismatch", member: "source/x264.tar.bz2", content: "pinned source", badOuter: true},
		{name: "wrong inner source", member: "source/x264.tar.bz2", content: "changed source"},
		{name: "missing inner source", member: "source/not-x264.tar.bz2", content: "pinned source"},
		{name: "release fetch failure", member: "source/x264.tar.bz2", content: "pinned source", fetchFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			gzipWriter := gzip.NewWriter(&archive)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := tarWriter.WriteHeader(&tar.Header{Name: tc.member, Mode: 0600, Size: int64(len(tc.content))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarWriter.Write([]byte(tc.content)); err != nil {
				t.Fatal(err)
			}
			binary := "#!/bin/sh\ntouch executable-ran\n"
			if err := tarWriter.WriteHeader(&tar.Header{Name: "ffmpeg/ffmpeg", Mode: 0700, Size: int64(len(binary))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarWriter.Write([]byte(binary)); err != nil {
				t.Fatal(err)
			}
			if err := tarWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			outerHash := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			innerHash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned source")))
			// Substitute only fixture pins; fetching, SHA verification and tar stay real.
			functions := strings.ReplaceAll(recipeFunctions(t), "b07ac21a558ee1cbf6f70f14f2e8d4f740e2c6eca464de8d964e5bc864c6dd66", outerHash)
			functions = strings.ReplaceAll(functions, "6eeb82934e69fd51e043bd8c5b0d152839638d1ce7aa4eea65a3fedcf83ff224", innerHash)
			directory := t.TempDir()
			data := archive.Bytes()
			if tc.badOuter {
				// A valid gzip with identical members but a changed OS header: skipping
				// the outer pin would accept it, so tar failure cannot hide that bug.
				data = bytes.Clone(data)
				data[9] ^= 1
			}
			if err := os.WriteFile(filepath.Join(directory, "fixture.tar.gz"), data, 0600); err != nil {
				t.Fatal(err)
			}
			script := functions + `
calls=0
fetch() {
 calls=$((calls+1))
 case "$2" in
  https://code.videolan.org/*)
   if test "$official_unavailable" = true; then return 1; fi
   if test "$calls" = "$official_ready_at"; then printf 'pinned source' > "$1";
   else printf '<html>challenge</html>' > "$1"; fi ;;
  https://github.com/HallelujahHomeChurch/hhc-cli/releases/download/v1.0.6/hhc_1.0.6_darwin_arm64.tar.gz)
   if test "$release_unavailable" = true; then return 1; fi
   cp fixture.tar.gz "$1" ;;
  *) return 90 ;;
 esac
}
if fetch_x264_source source.tar.bz2; then result=0; else result=1; fi
test "$calls" = "$expected_fetches" || exit 91
exit "$result"
`
			command := exec.Command(bash, "-c", script)
			command.Dir = directory
			expectedFetches := 3
			if tc.officialReadyAt != 0 {
				expectedFetches = tc.officialReadyAt
			}
			command.Env = append(os.Environ(), fmt.Sprintf("official_unavailable=%t", tc.officialUnavailable), fmt.Sprintf("release_unavailable=%t", tc.fetchFailure), fmt.Sprintf("official_ready_at=%d", tc.officialReadyAt), fmt.Sprintf("expected_fetches=%d", expectedFetches))
			output, err := command.CombinedOutput()
			if tc.success && err != nil {
				t.Fatalf("%v %s", err, output)
			}
			if !tc.success {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("not fail closed: %v %s", err, output)
				}
			}
			if tc.success {
				source, err := os.ReadFile(filepath.Join(directory, "source.tar.bz2"))
				if err != nil || string(source) != "pinned source" {
					t.Fatal("wrong source accepted", err)
				}
			}
			for _, path := range []string{"ffmpeg", "executable-ran"} {
				if _, err := os.Stat(filepath.Join(directory, path)); !os.IsNotExist(err) {
					t.Fatal("recovery extracted or ran bundled executable", path, err)
				}
			}
		})
	}
}
