package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) (Manifest, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: 1, Version: "1.2.3", BundleVersion: "8.1.3-hhc1", SkillVersion: "1.2.3", JournalSchema: 1}
	for _, platform := range []string{"windows/amd64", "darwin/arm64"} {
		ext := ".tar.gz"
		if platform == "windows/amd64" {
			ext = ".zip"
		}
		m.Artifacts = append(m.Artifacts, Artifact{Platform: platform, URL: "https://github.com/HallelujahHomeChurch/hhc-cli/releases/download/v1.2.3/hhc_1.2.3_" + strings.ReplaceAll(platform, "/", "_") + ext, Size: 1024, SHA256: strings.Repeat("a", 64)})
	}
	return m, pub, priv
}

func TestVerifyReleaseManifest(t *testing.T) {
	m, pub, priv := releaseFixture(t)
	data, _ := json.Marshal(m)
	sig := ed25519.Sign(priv, data)
	got, err := VerifyManifest(data, sig, pub, "windows/amd64", 1)
	if err != nil || got.Version != m.Version {
		t.Fatalf("valid manifest: %v", err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"unknown schema":     func(m *Manifest) { m.SchemaVersion = 2 },
		"journal upgrade":    func(m *Manifest) { m.JournalSchema = 2 },
		"missing platform":   func(m *Manifest) { m.Artifacts = m.Artifacts[1:] },
		"duplicate platform": func(m *Manifest) { m.Artifacts[1] = m.Artifacts[0] },
		"foreign host":       func(m *Manifest) { m.Artifacts[0].URL = "https://evil.invalid/package.zip" },
		"foreign repo":       func(m *Manifest) { m.Artifacts[0].URL = strings.Replace(m.Artifacts[0].URL, "hhc-cli/", "other/", 1) },
		"query":              func(m *Manifest) { m.Artifacts[0].URL += "?token=hidden" },
		"wrong release":      func(m *Manifest) { m.Artifacts[0].URL = strings.ReplaceAll(m.Artifacts[0].URL, "1.2.3", "1.2.4") },
		"oversize":           func(m *Manifest) { m.Artifacts[0].Size = 3 << 30 },
		"digest":             func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) },
		"prerelease":         func(m *Manifest) { m.Version = "1.2.3-rc1" },
		"skill mismatch":     func(m *Manifest) { m.SkillVersion = "1.2.2" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := m
			copy.Artifacts = append([]Artifact(nil), m.Artifacts...)
			mutate(&copy)
			raw, _ := json.Marshal(copy)
			if _, err := VerifyManifest(raw, ed25519.Sign(priv, raw), pub, "windows/amd64", 1); err == nil {
				t.Fatal("accepted invalid signed metadata")
			}
		})
	}
	for _, raw := range [][]byte{append(append([]byte(nil), data...), '\n'), []byte(`{}`), []byte(strings.Repeat("x", MaxManifestBytes+1))} {
		if _, err := VerifyManifest(raw, sig, pub, "windows/amd64", 1); err == nil {
			t.Fatal("accepted tampered payload")
		}
	}
	if _, err := VerifyManifest(data, sig, ed25519.PublicKey{1}, "windows/amd64", 1); err == nil {
		t.Fatal("invalid key")
	}
	if _, err := VerifyManifest(data, sig, pub, "linux/amd64", 1); err == nil {
		t.Fatal("unsupported platform")
	}
}

func TestStableVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		next, current string
		want          bool
		valid         bool
	}{
		{"1.10.0", "1.9.9", true, true}, {"2.0.0", "1.99.99", true, true}, {"1.2.3", "1.2.3", false, true}, {"1.2.2", "1.2.3", false, true},
		{"01.2.3", "1.0.0", false, false}, {"1.2.3", "dev", false, false}, {"1.2.3-rc1", "1.0.0", false, false}, {"999999999999999999999.0.0", "1.0.0", false, false},
	} {
		got, err := Newer(tc.next, tc.current)
		if got != tc.want || (err == nil) != tc.valid {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
}
