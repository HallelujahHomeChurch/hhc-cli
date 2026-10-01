package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fixtureEntry struct {
	name, body string
	kind       byte
}

func archiveFixture(t *testing.T, platform string, entries []fixtureEntry) (string, Artifact) {
	t.Helper()
	var out bytes.Buffer
	if platform == "windows/amd64" {
		w := zip.NewWriter(&out)
		for _, e := range entries {
			h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			h.SetMode(0600)
			if e.kind == tar.TypeSymlink {
				h.SetMode(os.ModeSymlink | 0777)
			}
			f, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&out)
		w := tar.NewWriter(gz)
		for _, e := range entries {
			kind := e.kind
			if kind == 0 {
				kind = tar.TypeReg
			}
			size := int64(len(e.body))
			if kind != tar.TypeReg {
				size = 0
			}
			if err := w.WriteHeader(&tar.Header{Name: e.name, Mode: 0600, Size: size, Typeflag: kind, Linkname: "outside"}); err != nil {
				t.Fatal(err)
			}
			if size > 0 {
				if _, err := w.Write([]byte(e.body)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "release.archive")
	if err := os.WriteFile(p, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return p, Artifact{Platform: platform, Size: int64(out.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(out.Bytes()))}
}
func validEntries(platform string) []fixtureEntry {
	suffix := ""
	if platform == "windows/amd64" {
		suffix = ".exe"
	}
	return []fixtureEntry{{"hhc" + suffix, "cli", 0}, {"ffmpeg/ffmpeg" + suffix, "encoder", 0}, {"ffmpeg/ffprobe" + suffix, "probe", 0}, {"skills/hhc/SKILL.md", "skill", 0}, {"skills/hhc/references/commands.md", "commands", 0}, {"licenses/COPYING.GPLv3", "license", 0}, {"source/build-ffmpeg.sh", "recipe", 0}, {"hhc-launcher" + suffix, "launcher", 0}}
}
func TestExtractAuthenticatedBundle(t *testing.T) {
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			entries := validEntries(platform)
			archive, a := archiveFixture(t, platform, entries)
			dest := filepath.Join(t.TempDir(), "version")
			if err := ExtractBundle(archive, dest, a); err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(e.name)))
				if err != nil || string(got) != e.body {
					t.Fatalf("%s: %v", e.name, err)
				}
			}
			if err := ExtractBundle(archive, dest, a); err == nil {
				t.Fatal("overwrote existing bundle")
			}
		})
	}
}
func TestExtractRejectsUnsafeArchive(t *testing.T) {
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		for _, bad := range []fixtureEntry{{"../escape", "x", 0}, {"/absolute", "x", 0}, {"source/a\\b", "x", 0}, {"source/CON", "x", 0}, {"source/a:stream", "x", 0}, {"source/a.", "x", 0}, {"source/link", "x", tar.TypeSymlink}, {"credentials.json", "x", 0}, {"skills/hhc/SKILL.md", "duplicate", 0}, {"skills/hhc/skill.md", "case collision", 0}} {
			t.Run(platform+bad.name, func(t *testing.T) {
				archive, a := archiveFixture(t, platform, append(validEntries(platform), bad))
				dest := filepath.Join(t.TempDir(), "version")
				if err := ExtractBundle(archive, dest, a); err == nil {
					t.Fatal("accepted unsafe archive")
				}
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatal("left partial install")
				}
			})
		}
		archive, a := archiveFixture(t, platform, validEntries(platform))
		a.SHA256 = "bad"
		dest := filepath.Join(t.TempDir(), "version")
		if err := ExtractBundle(archive, dest, a); err == nil {
			t.Fatal("accepted corrupted digest")
		}
		archive, a = archiveFixture(t, platform, validEntries(platform)[:1])
		if err := ExtractBundle(archive, dest, a); err == nil {
			t.Fatal("accepted incomplete bundle")
		}
	}
}
