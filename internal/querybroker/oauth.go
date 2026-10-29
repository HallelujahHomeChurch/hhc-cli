package querybroker

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func secureURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.RawQuery == "" && !u.ForceQuery && u.RawPath == "" && u.Opaque == "" && !strings.ContainsAny(raw, "\r\n\x00")
}
func oauthHTTP() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Introspector performs a fresh RFC7662 check for every protected operation;
// no JWT/authorization cache can outlive issuer revocation. An actual issuer
// with resource-bound access-token introspection is a deployment prerequisite.
type Introspector struct {
	issuer, endpoint, audience, scope, clientID, secret string
	http                                                *http.Client
}

func (Introspector) String() string     { return "[redacted introspection configuration]" }
func (v Introspector) GoString() string { return v.String() }
func NewIntrospector(issuer, endpoint, audience, scope, clientID, secret string) (*Introspector, error) {
	if !secureURL(issuer) || !secureURL(endpoint) || !secureURL(audience) || scope == "" || strings.ContainsAny(scope, " \r\n\t\x00\"\\") || clientID == "" || secret == "" || strings.ContainsAny(clientID+secret, "\r\n\x00") {
		return nil, ErrUnavailable
	}
	return &Introspector{issuer: issuer, endpoint: endpoint, audience: audience, scope: scope, clientID: clientID, secret: secret, http: oauthHTTP()}, nil
}
func (i *Introspector) Authenticate(ctx context.Context, bearer string) (Principal, error) {
	if len(bearer) < 16 || len(bearer) > 8192 || strings.ContainsAny(bearer, "\r\n\x00 ") {
		return Principal{}, ErrReauthorize
	}
	form := url.Values{"token": {bearer}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(i.clientID, i.secret)
	resp, err := i.http.Do(req)
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Principal{}, ErrUnavailable
	}
	raw, err := readJSON(resp, 16384)
	if err != nil {
		return Principal{}, err
	}
	defer clear(raw)
	var v struct {
		Active    bool            `json:"active"`
		Issuer    string          `json:"iss"`
		Subject   string          `json:"sub"`
		Scope     string          `json:"scope"`
		Audience  json.RawMessage `json:"aud"`
		Expires   int64           `json:"exp"`
		NotBefore int64           `json:"nbf"`
		TokenType string          `json:"token_type"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return Principal{}, ErrUnavailable
	}
	if !v.Active || v.Issuer != i.issuer || v.Subject == "" || len(v.Subject) > 256 || v.Expires <= time.Now().Unix() || v.NotBefore > time.Now().Unix() || !strings.EqualFold(v.TokenType, "Bearer") {
		return Principal{}, ErrReauthorize
	}
	var audiences []string
	var one string
	if json.Unmarshal(v.Audience, &one) == nil {
		audiences = []string{one}
	} else if json.Unmarshal(v.Audience, &audiences) != nil {
		return Principal{}, ErrReauthorize
	}
	// A token spanning another protected resource is deliberately not accepted.
	if len(audiences) != 1 || audiences[0] != i.audience {
		return Principal{}, ErrReauthorize
	}
	found := false
	for _, scope := range strings.Fields(v.Scope) {
		if scope == i.scope {
			found = true
		}
	}
	if !found {
		return Principal{}, ErrScope
	}
	return Principal{Issuer: v.Issuer, Subject: v.Subject, Scope: v.Scope, Audience: i.audience, ExpiresAt: time.Unix(v.Expires, 0)}, nil
}
func readJSON(resp *http.Response, limit int64) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrUnavailable
	}
	return raw, nil
}

// GoogleRefresh is the concrete youtube.readonly OAuth refresh adapter. No grant
// or credential is created here, and no ambiguous exchange is ever retried.
// HHC native refresh currently accepts recording scopes only; it is deliberately
// not reused for cms:pages:read or broadened by this integration.
type GoogleRefresh struct {
	http     *http.Client
	endpoint string
}

func NewGoogleRefresh() *GoogleRefresh {
	return &GoogleRefresh{http: oauthHTTP(), endpoint: "https://oauth2.googleapis.com/token"}
}
func (r *GoogleRefresh) Refresh(ctx context.Context, g Grant) (Secrets, time.Time, error) {
	if g.Source != "youtube" || g.Kind != "oauth" || g.Scope != "https://www.googleapis.com/auth/youtube.readonly" || g.Secrets.RefreshToken == "" || g.Secrets.ClientID == "" {
		return Secrets{}, time.Time{}, ErrReauthorize
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.Secrets.RefreshToken}, "client_id": {g.Secrets.ClientID}, "scope": {g.Scope}}
	if g.Secrets.ClientSecret != "" {
		form.Set("client_secret", g.Secrets.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Secrets{}, time.Time{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return Secrets{}, time.Time{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Secrets{}, time.Time{}, ErrReauthorize
	}
	raw, err := readJSON(resp, 16384)
	if err != nil {
		return Secrets{}, time.Time{}, err
	}
	defer clear(raw)
	var v struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Scope   string `json:"scope"`
		Expires int64  `json:"expires_in"`
		Type    string `json:"token_type"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Scope != g.Scope || !strings.EqualFold(v.Type, "Bearer") || v.Expires < 61 || v.Expires > 86400 || v.Access == "" || len(v.Access) > 8192 || !validSecrets(Secrets{AccessToken: v.Access, RefreshToken: v.Refresh}) {
		return Secrets{}, time.Time{}, ErrReauthorize
	}
	next := g.Secrets
	next.AccessToken = v.Access
	if v.Refresh != "" {
		next.RefreshToken = v.Refresh
	}
	return next, time.Now().Add(time.Duration(v.Expires) * time.Second), nil
}
