package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

type testTransport struct {
	base   http.RoundTripper
	server *url.URL
	t      *testing.T
}

func (r testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Referer") != "" {
		r.t.Fatal("credential-bearing update request")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.server.Scheme, r.server.Host
	clone.URL = &u
	return r.base.RoundTrip(clone)
}
func TestSignedReleaseDownload(t *testing.T) {
	m, pub, priv := releaseFixture(t)
	body := []byte("artifact fixture")
	m.Artifacts[0].Size = int64(len(body))
	m.Artifacts[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	data, _ := json.Marshal(m)
	corrupt := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/HallelujahHomeChurch/hhc-cli/releases/latest/download/release.json":
			w.Write(data)
		case "/HallelujahHomeChurch/hhc-cli/releases/latest/download/release.json.sig":
			w.Write(ed25519.Sign(priv, data))
		default:
			if corrupt {
				w.Write([]byte("corrupt"))
			} else {
				w.Write(body)
			}
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	c := newReleaseClient()
	c.Transport = testTransport{server.Client().Transport, u, t}
	got, available, err := checkRelease(context.Background(), c, pub, "windows/amd64", "1.0.0")
	if err != nil || !available || got.Version != m.Version {
		t.Fatalf("check: %v %v", available, err)
	}
	path := filepath.Join(t.TempDir(), "download.zip")
	if err := downloadArtifact(context.Background(), c, m.Artifacts[0], path); err != nil {
		t.Fatal(err)
	}
	if err := downloadArtifact(context.Background(), c, m.Artifacts[0], path); err == nil {
		t.Fatal("overwrote download")
	}
	corrupt = true
	bad := filepath.Join(t.TempDir(), "bad.zip")
	if err := downloadArtifact(context.Background(), c, m.Artifacts[0], bad); err == nil {
		t.Fatal("accepted bad bytes")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("left corrupted download")
	}
}
func TestDownloadRedirectAllowlist(t *testing.T) {
	c := newReleaseClient()
	previous, _ := http.NewRequest("GET", "https://github.com/HallelujahHomeChurch/hhc-cli/releases/latest/download/release.json", nil)
	for _, raw := range []string{"http://github.com/HallelujahHomeChurch/hhc-cli/releases/download/v1.0.0/release.json", "https://evil.invalid/file", "https://github.com/other/repo/releases/download/v1.0.0/release.json", "https://user@release-assets.githubusercontent.com/file", "https://release-assets.githubusercontent.com:444/file"} {
		req, _ := http.NewRequest("GET", raw, nil)
		if c.CheckRedirect(req, []*http.Request{previous}) == nil {
			t.Fatalf("allowed %s", raw)
		}
	}
	req, _ := http.NewRequest("GET", "https://release-assets.githubusercontent.com/github-production-release-asset/1/file?signature=ephemeral", nil)
	if err := c.CheckRedirect(req, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
}
