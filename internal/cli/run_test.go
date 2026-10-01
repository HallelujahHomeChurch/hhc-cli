package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func TestPreparationErrorsHaveSafeActionableCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{bundle.ErrUnavailable, "ffmpeg_bundle_unavailable"},
		{media.ErrLocalCleanup, "local_cleanup_failed"},
		{media.ErrInsufficientDisk, "insufficient_disk_space"},
	} {
		code, _, exit, _ := classify(tc.err)
		if code != tc.code || exit == 0 {
			t.Fatalf("error mapping: %s %d", code, exit)
		}
	}
}

func TestStandalonePrepareDoesNotRequireLoginOrProfile(t *testing.T) {
	id := fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	directory, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		directory, err = os.UserCacheDir()
	}
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(directory, "HHC", "cli", "operations", id)
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("fixture ID collision")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(owned); err != nil {
			t.Error(err)
		}
	})
	parent := t.TempDir()
	var out, diagnostics bytes.Buffer
	code := Run(context.Background(), []string{"recordings", "prepare", filepath.Join(parent, "source.mp4"), "--output", filepath.Join(parent, "package"), "--operation-id", id, "--json", "--no-input"}, nil, &out, &diagnostics, "test")
	var value result
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if code != 5 || value.Error == nil || value.Error.Code != "ffmpeg_bundle_unavailable" || value.Profile != nil || value.Principal != nil {
		t.Fatalf("local prepare: %d %s", code, out.String())
	}
}

func TestJSONLoginNeverPromptsAndInvalidArgsAreRedacted(t *testing.T) {
	for _, tc := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"auth", "login", "--json"}, 3, "authentication_required"},
		{[]string{"auth", "login", "--service-principal", "--client-id", "uploader", "--json"}, 2, "invalid_input"},
		{[]string{"auth", "login", "--secret", "must-not-echo", "--json"}, 2, "invalid_input"},
		{[]string{"auth", "status", "--profile", "../must-not-echo", "--json"}, 2, "invalid_input"},
	} {
		var out, diagnostics bytes.Buffer
		exit := Run(context.Background(), tc.args, nil, &out, &diagnostics, "test")
		var result struct {
			SchemaVersion int
			OK            bool
			Error         struct{ Code string }
		}
		decoder := json.NewDecoder(&out)
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if exit != tc.exit || result.SchemaVersion != 1 || result.OK || result.Error.Code != tc.code {
			t.Fatalf("%v: exit=%d result=%+v", tc.args[:2], exit, result)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || strings.Contains(diagnostics.String(), "must-not-echo") {
			t.Fatal("unsafe output")
		}
	}
}

type tokenFixtureTransport struct {
	t         *testing.T
	origin    *url.URL
	transport http.RoundTripper
}

func (f tokenFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	account := r.URL.Host == "account.alive.org.tw" && r.URL.Path == "/api/account/v1/oauth/token"
	recording := r.URL.Host == "admin.alive.org.tw" && r.URL.Path == "/api/admin/recordings/00000000-0000-4000-8000-000000000041"
	if r.URL.Scheme != "https" || !account && !recording {
		f.t.Error("unexpected authentication destination")
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = f.origin.Scheme, f.origin.Host
	clone.Host = f.origin.Host
	return f.transport.RoundTrip(clone)
}

func TestNativeServiceCommandsUseCredentialStoreAndRedactSecret(t *testing.T) {
	if os.Getenv("HHC_TEST_NATIVE_CREDENTIALS") != "1" {
		t.Skip("requires native credential fixture opt-in")
	}
	// This test accesses only its unique dummy credential, never a real profile.
	profile := fmt.Sprintf("test-cli-%d", time.Now().UnixNano())
	store := auth.NativeStore{}
	t.Cleanup(func() {
		if err := store.Delete(profile); err != nil {
			t.Error(err)
		}
	})
	base, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		base, err = os.UserCacheDir()
	}
	if err != nil {
		t.Fatal(err)
	}
	fixtureDirectory := filepath.Join(base, "HHC", "cli", "profiles", profile)
	t.Cleanup(func() {
		entries, err := os.ReadDir(fixtureDirectory)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		for _, entry := range entries {
			if err := os.Remove(filepath.Join(fixtureDirectory, entry.Name())); err != nil {
				t.Error(err)
			}
		}
		if err := os.Remove(fixtureDirectory); err != nil {
			t.Error(err)
		}
	})
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/recordings/00000000-0000-4000-8000-000000000041" {
			if r.Header.Get("Authorization") != "Bearer dummy-access-never-print" || r.Method != "GET" {
				t.Error("incorrect protected read")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":{"id":"00000000-0000-4000-8000-000000000041","title":"主日聚會","status":"draft","version":1,"internalSecret":"never-print"},"error":null}`)
			return
		}
		client, secret, ok := r.BasicAuth()
		if !ok || client != "fixture" || secret != "dummy-secret-never-print" {
			t.Error("wrong credential request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"access_token":"dummy-access-never-print","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read","principal":{"type":"service","id":"00000000-0000-4000-8000-000000000011","client_id":"fixture","credential_id":"00000000-0000-4000-8000-000000000012","credential_expires_at":%q}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer s.Close()
	origin, _ := url.Parse(s.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = tokenFixtureTransport{t, origin, s.Client().Transport}
	defer func() { http.DefaultTransport = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer.WriteString("dummy-secret-never-print\n")
	writer.Close()
	for _, args := range [][]string{
		{"auth", "login", "--service-principal", "--client-id", "fixture", "--secret-stdin", "--scope", "cms:recordings:read"},
		{"auth", "status"}, {"recordings", "get", "00000000-0000-4000-8000-000000000041"}, {"auth", "logout"},
	} {
		var out, diagnostics bytes.Buffer
		args = append(args, "--profile", profile, "--json")
		exit := Run(context.Background(), args, reader, &out, &diagnostics, "test")
		if exit != 0 || strings.Contains(out.String()+diagnostics.String(), "never-print") {
			t.Fatalf("command failed or exposed credential: exit=%d", exit)
		}
		if args[1] != "logout" && !strings.Contains(out.String(), `"id":"00000000-0000-4000-8000-000000000011"`) {
			t.Fatal("missing confirmed identity")
		}
		if args[1] == "get" {
			out.Reset()
			code := Run(context.Background(), []string{"recordings", "upload", t.TempDir(), "--title", "Fixture", "--operation-id", "00000000-0000-4000-8000-000000000081", "--profile", profile, "--json", "--no-input"}, nil, &out, &diagnostics, "test")
			if code != 4 || !strings.Contains(out.String(), `"code":"permission_denied"`) {
				t.Fatalf("upload did not preflight scope: %d %s", code, out.String())
			}
			out.Reset()
			code = Run(context.Background(), []string{"recordings", "upload", filepath.Join(t.TempDir(), "source.mp4"), "--prepare", "--title", "Fixture", "--operation-id", "00000000-0000-4000-8000-000000000083", "--profile", profile, "--json", "--no-input"}, nil, &out, &diagnostics, "test")
			if code != 4 {
				t.Fatalf("prepare upload did not preflight scope: %d %s", code, out.String())
			}
			out.Reset()
			code = Run(context.Background(), []string{"recordings", "publish", "00000000-0000-4000-8000-000000000041", "--operation-id", "00000000-0000-4000-8000-000000000082", "--profile", profile, "--json", "--no-input"}, nil, &out, &diagnostics, "test")
			if code != 4 || !strings.Contains(out.String(), `"code":"permission_denied"`) {
				t.Fatalf("publish did not preflight scope: %d %s", code, out.String())
			}
		}
	}
	if _, err := store.Load(profile); err != auth.ErrCredentialNotFound {
		t.Fatal("logout retained credential")
	}
}

func TestVersionJSONIsOfflineAndCancellationIsNotSuccess(t *testing.T) {
	var out bytes.Buffer
	if Run(context.Background(), []string{"version", "--json"}, nil, &out, io.Discard, "1.2.3") != 0 || !strings.Contains(out.String(), `"version":"1.2.3"`) {
		t.Fatal(out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if Run(ctx, []string{"auth", "status", "--json"}, nil, &out, io.Discard, "test") != 130 || !strings.Contains(out.String(), `"code":"cancelled"`) {
		t.Fatal(out.String())
	}
}

func TestSecretInputIsOneBoundedLine(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"secret\r\n", "secret", true}, {"secret", "secret", true},
		{"\n", "", false}, {strings.Repeat("s", 4097), "", false}, {"secret\x00\n", "", false},
	} {
		got, err := readSecretLine(strings.NewReader(tc.input))
		if (err == nil) != tc.valid || string(got) != tc.want {
			t.Fatal("incorrect secret input validation")
		}
	}
}
