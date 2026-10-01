package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrReleaseUnavailable = errors.New("release_unavailable")

const releaseBase = "https://github.com/HallelujahHomeChurch/hhc-cli/releases/"

var releaseDownloadPath = regexp.MustCompile(`^/HallelujahHomeChurch/hhc-cli/releases/(latest/download|download/v[0-9]+\.[0-9]+\.[0-9]+)/[a-zA-Z0-9_.-]+$`)

func newReleaseClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Minute, Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !allowedReleaseURL(req.URL, true) {
			return ErrInvalidRelease
		}
		// Public distribution only. Neither Account credentials nor GitHub tokens
		// are accepted, stored, or forwarded by this client.
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Referer")
		return nil
	}}
}

func allowedReleaseURL(u *url.URL, redirect bool) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if u.Host == "github.com" {
		return u.RawQuery == "" && releaseDownloadPath.MatchString(u.Path)
	}
	return redirect && u.Host == "release-assets.githubusercontent.com" && strings.HasPrefix(u.Path, "/github-production-release-asset/")
}

func releaseGET(ctx context.Context, c *http.Client, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !allowedReleaseURL(u, false) {
		return nil, ErrInvalidRelease
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, ErrInvalidRelease
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "hhc-cli-updater")
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrReleaseUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, ErrReleaseUnavailable
	}
	return resp, nil
}

func checkRelease(ctx context.Context, c *http.Client, key ed25519.PublicKey, platform, current string) (Manifest, bool, error) {
	var m Manifest
	read := func(name string, limit int64) ([]byte, error) {
		resp, err := releaseGET(ctx, c, releaseBase+"latest/download/"+name)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil || int64(len(data)) > limit {
			return nil, ErrReleaseUnavailable
		}
		return data, nil
	}
	data, err := read("release.json", MaxManifestBytes)
	if err != nil {
		return m, false, err
	}
	sig, err := read("release.json.sig", ed25519.SignatureSize)
	if err != nil {
		return m, false, err
	}
	m, err = VerifyManifest(data, sig, key, platform, 1)
	if err != nil {
		return Manifest{}, false, err
	}
	newer, err := Newer(m.Version, current)
	return m, newer, err
}

func downloadArtifact(ctx context.Context, c *http.Client, a Artifact, destination string) (err error) {
	if !filepath.IsAbs(destination) || a.Size <= 0 || a.Size > MaxArtifactBytes {
		return ErrInvalidRelease
	}
	digest, err := hex.DecodeString(a.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != a.SHA256 {
		return ErrInvalidRelease
	}
	resp, err := releaseGET(ctx, c, a.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength >= 0 && resp.ContentLength != a.Size {
		return ErrInvalidRelease
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			err = errors.Join(err, os.Remove(destination))
		}
	}()
	h := sha256.New()
	n, copyErr := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(resp.Body, a.Size+1), make([]byte, 64<<10))
	if copyErr != nil || n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrInvalidRelease
	}
	return f.Sync()
}
