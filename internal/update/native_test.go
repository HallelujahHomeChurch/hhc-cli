package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

func TestMain(m *testing.M) {
	if handled, status := operation.HandleSupervisor(os.Args[1:]); handled {
		os.Exit(status)
	}
	os.Exit(m.Run())
}

// Real compiled CLI/self-check/launcher, but tiny non-executed tool fixtures.
// This does not establish release signing or credential access across signed apps.
func TestNativeVersionSwitchAndCorruptToolRollback(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("supported native updater platforms")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	entries := validEntries(platform)
	toolFile := func(body string) bundle.File {
		return bundle.File{SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), SizeBytes: int64(len(body))}
	}
	embedded := bundle.Manifest{SchemaVersion: 1, BundleVersion: "8.1.3-hhc1", Platform: platform, FFmpeg: toolFile("encoder"), FFprobe: toolFile("probe")}
	data, _ := json.Marshal(embedded)
	compiled := filepath.Join(t.TempDir(), "hhc")
	if runtime.GOOS == "windows" {
		compiled += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=1.2.3 -X github.com/HallelujahHomeChurch/hhc-cli/internal/bundle.manifestBase64="+base64.StdEncoding.EncodeToString(data), "-o", compiled, "./cmd/hhc")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	binary, err := os.ReadFile(compiled)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].body = string(binary)
	launcher := filepath.Join(t.TempDir(), "hhc-launcher")
	if runtime.GOOS == "windows" {
		launcher += ".exe"
	}
	build = exec.Command("go", "build", "-o", launcher, "./cmd/hhc-launcher")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("launcher fixture: %v %s", err, out)
	}
	launcherBytes, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	entries[7].body = string(launcherBytes)
	for _, corrupt := range []bool{false, true} {
		root := installedFixture(t)
		m, _, _ := releaseFixture(t)
		candidate := append([]fixtureEntry(nil), entries...)
		if corrupt {
			candidate[1].body = "corrupt encoder"
		}
		archive, a := archiveFixture(t, platform, candidate)
		if err := installDownloaded(context.Background(), root, m, a, archive, selfCheck); (err != nil) != corrupt {
			t.Fatalf("corrupt=%v: %v", corrupt, err)
		}
		pointer, err := ReadPointer(root)
		if err != nil {
			t.Fatal(err)
		}
		if corrupt {
			if pointer.Current != "1.0.0" {
				t.Fatal("corrupt tools changed current version")
			}
			continue
		}
		if pointer.Current != "1.2.3" || pointer.Previous != "1.0.0" {
			t.Fatalf("pointer: %+v", pointer)
		}
		output := filepath.Join(t.TempDir(), "out.json")
		f, err := os.Create(output)
		if err != nil {
			t.Fatal(err)
		}
		exit, err := Launch(root, []string{"version", "--self-check", "--json"}, nil, f, f)
		f.Close()
		if err != nil || exit != 0 {
			t.Fatalf("updated launch: %d %v", exit, err)
		}
		response, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			OK   bool
			Data struct{ Version string }
		}
		if json.Unmarshal(response, &got) != nil || !got.OK || got.Data.Version != "1.2.3" {
			t.Fatalf("updated binary: %s", response)
		}
		initial := filepath.Join(t.TempDir(), "managed")
		if err := bootstrapDownloaded(context.Background(), initial, m, a, archive, selfCheck); err != nil {
			t.Fatalf("native bootstrap: %v", err)
		}
		entrypoint := filepath.Join(initial, "hhc")
		if runtime.GOOS == "windows" {
			entrypoint += ".exe"
		}
		response, err = exec.Command(entrypoint, "version", "--self-check", "--json").CombinedOutput()
		if err != nil || json.Unmarshal(response, &got) != nil || !got.OK || got.Data.Version != "1.2.3" {
			t.Fatalf("installed entrypoint: %v %s", err, response)
		}
		interrupted := installedFixture(t)
		if err := ExtractBundle(archive, filepath.Join(interrupted, "versions", m.Version), a); err != nil {
			t.Fatal(err)
		}
		if err := installDownloaded(context.Background(), interrupted, m, a, archive, selfCheck); err != nil {
			t.Fatalf("native interrupted switch: %v", err)
		}
		if runtime.GOOS != "windows" {
			unexecutable := installedFixture(t)
			target := filepath.Join(unexecutable, "versions", m.Version)
			if err := ExtractBundle(archive, target, a); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(target, "hhc"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := installDownloaded(context.Background(), unexecutable, m, a, archive, selfCheck); err == nil {
				t.Fatal("selected an unexecutable target")
			}
			p, _ := ReadPointer(unexecutable)
			if p.Current != "1.0.0" {
				t.Fatal("unexecutable target changed pointer")
			}
		}
	}
}
