// Package querybroker owns caller authorization and durable encrypted grant,
// quota and refresh coordination. It has no content mutation operations.
package querybroker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"
)

var (
	ErrDenied        = errors.New("permission_denied")
	ErrUnavailable   = errors.New("broker_unavailable")
	ErrReauthorize   = errors.New("authentication_required")
	ErrQuota         = errors.New("rate_limited")
	ErrScope         = errors.New("insufficient_scope")
	ErrCallerChanged = errors.New("caller_authority_changed")
)

type Principal struct {
	Issuer, Subject, Scope, Audience string
	ExpiresAt                        time.Time
}

func CallerID(p Principal) string {
	h := sha256.Sum256([]byte(p.Issuer + "\x00" + p.Subject))
	return hex.EncodeToString(h[:])
}

type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
type Policy struct {
	CallerID                                           string
	Revision                                           int64
	Enabled                                            bool
	HHCMode, HHCGrant, HHCQuota                        string
	YouTubeMode, YouTubeGrant, YouTubeQuota, ChannelID string
	RequestsPerMinute                                  int
}
type Grant struct {
	ID, Owner, Source, Resource, Kind, Scope string
	Generation                               int64
	State                                    string
	ExpiresAt                                time.Time
	Secrets                                  Secrets
}
type Secrets struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

func (Secrets) String() string     { return "[redacted secrets]" }
func (s Secrets) GoString() string { return s.String() }
func (Grant) String() string       { return "[redacted grant]" }
func (g Grant) GoString() string   { return g.String() }

type Refresher interface {
	Refresh(context.Context, Grant) (Secrets, time.Time, error)
}
type Store interface {
	Ready(context.Context) error
	Policy(context.Context, string) (Policy, error)
	Permit(context.Context, Policy, string, string, string, int) error
	Credential(context.Context, Policy, string, string, Refresher) (Grant, error)
}
type Vault struct {
	aead       cipher.AEAD
	bindingKey []byte
}

func NewVault(master []byte, resource string) (*Vault, error) {
	u, err := url.Parse(resource)
	if len(master) != 32 || !secureURL(resource) || err != nil || u.Path != "/mcp" {
		return nil, ErrUnavailable
	}
	derive := func(label string) []byte {
		h := hmac.New(sha256.New, master)
		h.Write([]byte(resource + "\x00" + label))
		return h.Sum(nil)
	}
	key := derive("hhc-query/grant-encryption/v1")
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Vault{aead: aead, bindingKey: derive("hhc-query/request-bindings/v1")}, nil
}
func (v *Vault) seal(g Grant) ([]byte, error) {
	raw, err := json.Marshal(g.Secrets)
	if err != nil || len(raw) > 32768 {
		return nil, ErrDenied
	}
	defer clear(raw)
	nonce := make([]byte, v.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ErrUnavailable
	}
	return v.aead.Seal(nonce, nonce, raw, grantAAD(g)), nil
}
func (v *Vault) open(g *Grant, raw []byte) error {
	n := v.aead.NonceSize()
	if len(raw) < n || len(raw) > 65536 {
		return ErrUnavailable
	}
	plain, err := v.aead.Open(nil, raw[:n], raw[n:], grantAAD(*g))
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plain)
	if json.Unmarshal(plain, &g.Secrets) != nil {
		return ErrUnavailable
	}
	return nil
}
func grantAAD(g Grant) []byte {
	raw, _ := json.Marshal([]any{"hhc-query/grant/v1", g.ID, g.Owner, g.Source, g.Resource, g.Kind, g.Scope, g.Generation, g.State, g.ExpiresAt.UnixNano()})
	return raw
}

func (v *Vault) Fingerprint() string {
	h := sha256.Sum256(v.bindingKey)
	return hex.EncodeToString(h[:])
}

func callerFailure(err error) error { return errors.Join(ErrCallerChanged, err) }

func (v *Vault) refreshIdentity(g Grant) []byte {
	mac := hmac.New(sha256.New, v.bindingKey)
	mac.Write([]byte("refresh-identity/v1\x00" + g.Source + "\x00" + g.Secrets.ClientID + "\x00" + g.Secrets.RefreshToken))
	return mac.Sum(nil)
}
