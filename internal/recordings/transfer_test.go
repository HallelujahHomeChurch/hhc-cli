package recordings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

type fixtureTransport struct {
	origin    *url.URL
	transport http.RoundTripper
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = f.origin.Scheme, f.origin.Host
	r.Host = f.origin.Host
	return f.transport.RoundTrip(r)
}

func transferFixture(t *testing.T) (*os.File, media.RecordingPackageObject, SignedObject) {
	t.Helper()
	content := []byte("small bounded fixture")
	name := filepath.Join(t.TempDir(), "segment")
	if err := os.WriteFile(name, content, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	hash := sha256.Sum256(content)
	object := media.RecordingPackageObject{Path: "480p/seg-000000.m4s", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
	query := url.Values{"X-Amz-Date": {time.Now().UTC().Format("20060102T150405Z")}, "X-Amz-Expires": {"900"}, "X-Amz-SignedHeaders": {"content-length;content-type;host"}, "X-Amz-Signature": {strings.Repeat("a", 64)}}
	target := SignedObject{Path: object.Path, Method: "PUT", URL: "https://" + strings.Repeat("a", 32) + ".r2.cloudflarestorage.com/test-bucket/recordings/packages/" + strings.Repeat("b", 32) + "/staging/" + object.Path + "?" + query.Encode(), Headers: http.Header{"Content-Type": {"video/mp4"}, "Content-Length": {"21"}}}
	return f, object, target
}

func TestObjectTransferDoesNotForwardIdentityOrFollowRedirects(t *testing.T) {
	for _, status := range []int{200, 307, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			file, object, target := transferFixture(t)
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != "PUT" || r.ContentLength != object.SizeBytes || string(body) != "small bounded fixture" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("wrong transfer or credential leak")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(status)
				io.WriteString(w, "never-echo-signed-url")
			}))
			defer s.Close()
			u := NewUploader()
			origin, _ := url.Parse(s.URL)
			u.http.Transport = fixtureTransport{origin, s.Client().Transport}
			err := u.Put(context.Background(), file, strings.Repeat("b", 32), object, target)
			if calls != 1 || (err == nil) != (status == 200) || err != nil && strings.Contains(err.Error(), "never-echo") {
				t.Fatalf("unsafe transfer result: calls=%d err=%v", calls, err)
			}
			if status == 403 && !errors.Is(err, ErrUploadURLRejected) {
				t.Fatal("URL rejection must not become API identity fallback")
			}
		})
	}
}

func TestObjectTransferRejectsChangedBytesAndUnsafeTargetsBeforeNetwork(t *testing.T) {
	for _, change := range []string{"checksum", "authorization", "host", "package", "path", "size", "expired", "overlong", "duplicate", "encoded-path", "userinfo", "method"} {
		t.Run(change, func(t *testing.T) {
			file, object, target := transferFixture(t)
			switch change {
			case "checksum":
				object.SHA256 = strings.Repeat("0", 64)
			case "authorization":
				target.Headers["Authorization"] = []string{"do-not-forward"}
			case "host":
				target.URL = strings.Replace(target.URL, ".r2.cloudflarestorage.com", ".r2.cloudflarestorage.com.attacker.test", 1)
			case "package":
				target.URL = strings.Replace(target.URL, strings.Repeat("b", 32), strings.Repeat("c", 32), 1)
			case "path":
				target.Path = "../source.mp4"
			case "size":
				object.SizeBytes++
			case "expired":
				parsed, _ := url.Parse(target.URL)
				query := parsed.Query()
				query.Set("X-Amz-Date", time.Now().Add(-time.Hour).UTC().Format("20060102T150405Z"))
				parsed.RawQuery = query.Encode()
				target.URL = parsed.String()
			case "overlong":
				target.URL = strings.Replace(target.URL, "X-Amz-Expires=900", "X-Amz-Expires=901", 1)
			case "duplicate":
				target.URL += "&X-Amz-Expires=900"
			case "encoded-path":
				target.URL = strings.Replace(target.URL, "/staging/", "/%73taging/", 1)
			case "userinfo":
				target.URL = strings.Replace(target.URL, "https://", "https://user@", 1)
			case "method":
				target.Method = "POST"
			}
			u := NewUploader()
			u.http.Transport = rejectTransport{t}
			if err := u.Put(context.Background(), file, strings.Repeat("b", 32), object, target); err == nil {
				t.Fatal("accepted unsafe transfer")
			}
		})
	}
}

func TestSignedUploadCapabilityCannotLeakThroughJSON(t *testing.T) {
	_, _, target := transferFixture(t)
	encoded, err := json.Marshal(target)
	if err != nil || strings.Contains(string(encoded), "https:") || strings.Contains(string(encoded), "X-Amz") {
		t.Fatal("upload URL is JSON-visible")
	}
}

type rejectTransport struct{ t *testing.T }

func (f rejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("unexpected network request")
	return nil, errors.New("blocked")
}
