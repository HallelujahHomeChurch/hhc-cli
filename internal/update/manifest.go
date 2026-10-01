// Package update verifies the immutable, signed release contract before any
// artifact may be downloaded or executed. It has no credential dependencies.
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const MaxManifestBytes = 32 << 10
const MaxArtifactBytes = 2 << 30

var ErrInvalidRelease = errors.New("invalid_release")
var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
var bundleVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,63}$`)

type Artifact struct {
	Platform string `json:"platform"`
	URL      string `json:"artifactURL"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	Version       string     `json:"version"`
	JournalSchema int        `json:"journalSchema"`
	BundleVersion string     `json:"bundleVersion"`
	SkillVersion  string     `json:"skillVersion"`
	Artifacts     []Artifact `json:"artifacts"`
}

// VerifyManifest authenticates exact bytes, not re-marshaled JSON. Public keys
// must come from the installed trusted binary, never the downloaded manifest.
func VerifyManifest(data, signature []byte, key ed25519.PublicKey, platform string, journalSchema int) (Manifest, error) {
	var m Manifest
	if len(data) == 0 || len(data) > MaxManifestBytes || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, data, signature) {
		return m, ErrInvalidRelease
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return Manifest{}, ErrInvalidRelease
	}
	var extra any
	if d.Decode(&extra) != io.EOF || m.SchemaVersion != 1 || !stableVersion.MatchString(m.Version) || m.SkillVersion != m.Version || !bundleVersion.MatchString(m.BundleVersion) || m.JournalSchema != journalSchema || journalSchema != 1 || len(m.Artifacts) != 2 {
		return Manifest{}, ErrInvalidRelease
	}
	seen := make(map[string]bool, 2)
	for _, a := range m.Artifacts {
		ext := ".tar.gz"
		switch a.Platform {
		case "windows/amd64":
			ext = ".zip"
		case "darwin/arm64":
		default:
			return Manifest{}, ErrInvalidRelease
		}
		// Exact canonical URL excludes alternate owners, escapes, query secrets,
		// userinfo, tags and redirect injection in manifest-controlled inputs.
		expected := "https://github.com/HallelujahHomeChurch/hhc-cli/releases/download/v" + m.Version + "/hhc_" + m.Version + "_" + strings.ReplaceAll(a.Platform, "/", "_") + ext
		digest, err := hex.DecodeString(a.SHA256)
		if seen[a.Platform] || a.URL != expected || a.Size <= 0 || a.Size > MaxArtifactBytes || err != nil || len(digest) != 32 || hex.EncodeToString(digest) != a.SHA256 {
			return Manifest{}, ErrInvalidRelease
		}
		seen[a.Platform] = true
	}
	if !seen[platform] {
		return Manifest{}, ErrInvalidRelease
	}
	return m, nil
}

// Newer accepts only stable versions and never permits a downgrade. Portable
// development versions require installation, not a guessed ordering.
func Newer(next, current string) (bool, error) {
	if !stableVersion.MatchString(next) || !stableVersion.MatchString(current) {
		return false, ErrInvalidRelease
	}
	a, b := strings.Split(next, "."), strings.Split(current, ".")
	for i := range a {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x > y, nil
		}
	}
	return false, nil
}
