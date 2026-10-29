package videoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

const (
	googleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL     = "https://oauth2.googleapis.com/token"
	googleRevokeURL    = "https://oauth2.googleapis.com/revoke"
	googleChannelsURL  = "https://www.googleapis.com/youtube/v3/channels"
)

// GoogleOAuthConfig describes an already approved OAuth client, callback and
// channel. It does not register clients or establish that a callback exists.
// Scope defaults to readonly; editing requires explicitly selecting force-ssl.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	ClientType   string // desktop or web
	RedirectURI  string
	ChannelID    string
	Scope        string
}

func (GoogleOAuthConfig) String() string     { return "[redacted Google OAuth configuration]" }
func (c GoogleOAuthConfig) GoString() string { return c.String() }

// GoogleOAuth is provider code, not a browser, listener, credential store or
// execution service. The owner must retain the session in memory across the
// browser interaction and securely persist the returned grant before using it.
type GoogleOAuth struct {
	config                           GoogleOAuthConfig
	http                             *http.Client
	tokenURL, revokeURL, channelsURL string
	now                              func() time.Time
	random                           io.Reader
}

func (GoogleOAuth) String() string     { return "[redacted Google OAuth client]" }
func (c GoogleOAuth) GoString() string { return c.String() }

func NewGoogleOAuth(config GoogleOAuthConfig) (*GoogleOAuth, error) {
	if config.Scope == "" {
		config.Scope = youtube.ReadScope
	}
	if !validGoogleScope(config.Scope) || !safeToken(config.ClientID) || len(config.ClientID) > 512 || config.ClientSecret != "" && !safeToken(config.ClientSecret) || !youtube.ValidChannel(config.ChannelID) {
		return nil, videos.ErrInput
	}
	u, err := url.Parse(config.RedirectURI)
	if err != nil || len(config.RedirectURI) > 2048 || u.User != nil || u.Host == "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || u.RawPath != "" || u.Path == "" || strings.ContainsAny(config.RedirectURI, "\r\n\x00") {
		return nil, videos.ErrInput
	}
	switch config.ClientType {
	case "desktop":
		port, e := strconv.Atoi(u.Port())
		if u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || e != nil || port < 1 || port > 65535 {
			return nil, videos.ErrInput
		}
	case "web":
		if u.Scheme != "https" || !safeToken(config.ClientSecret) {
			return nil, videos.ErrInput
		}
	default:
		return nil, videos.ErrInput
	}
	return &GoogleOAuth{config: config, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tokenURL: googleTokenURL, revokeURL: googleRevokeURL, channelsURL: googleChannelsURL, now: time.Now, random: rand.Reader}, nil
}

// GoogleSession contains process-local PKCE/state. It cannot be serialized into a
// later CLI invocation. Expired, denied, exchanged or uncertain sessions are spent.
type GoogleSession struct {
	data *googleSessionData
}

type googleSessionData struct {
	mu              sync.Mutex
	owner           *GoogleOAuth
	state, verifier string
	expiresAt       time.Time
	consumed        bool
}

func (GoogleSession) String() string     { return "[redacted Google OAuth session]" }
func (s GoogleSession) GoString() string { return s.String() }

func (g *GoogleOAuth) Begin(ctx context.Context) (*GoogleSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw := make([]byte, 64)
	defer clear(raw)
	if _, err := io.ReadFull(g.random, raw); err != nil {
		return nil, videos.FailureCode("oauth_unavailable")
	}
	return &GoogleSession{data: &googleSessionData{owner: g, state: base64.RawURLEncoding.EncodeToString(raw[:32]), verifier: base64.RawURLEncoding.EncodeToString(raw[32:]), expiresAt: g.now().Add(10 * time.Minute)}}, nil
}

// AuthorizationURL is intentionally explicit: deliver it only to the consenting
// user through the approved browser integration, not logs or generic output.
func (s *GoogleSession) AuthorizationURL() string {
	if s == nil || s.data == nil {
		return ""
	}
	d := s.data
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.consumed || !d.expiresAt.After(d.owner.now()) {
		return ""
	}
	challenge := sha256.Sum256([]byte(d.verifier))
	c := d.owner.config
	q := url.Values{"client_id": {c.ClientID}, "redirect_uri": {c.RedirectURI}, "response_type": {"code"}, "scope": {c.Scope}, "state": {d.state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"false"}}
	return googleAuthorizeURL + "?" + q.Encode()
}

func (s *GoogleSession) ExpiresAt() time.Time {
	if s == nil || s.data == nil {
		return time.Time{}
	}
	d := s.data
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expiresAt
}

// Complete takes the callback directly from its approved receiver, not a pasted
// authorization code. It validates exact callback/client/state and consumes the
// session before network I/O. No failed/ambiguous exchange may replay that code.
func (g *GoogleOAuth) Complete(ctx context.Context, s *GoogleSession, callbackURL string) (GoogleCredential, error) {
	if err := ctx.Err(); err != nil {
		return GoogleCredential{}, err
	}
	if s == nil || s.data == nil || len(callbackURL) > 16384 {
		return GoogleCredential{}, videos.ErrInput
	}
	d := s.data
	d.mu.Lock()
	if d.owner != g {
		d.mu.Unlock()
		return GoogleCredential{}, videos.FailureCode("oauth_session_invalid")
	}
	if d.consumed || !d.expiresAt.After(g.now()) {
		d.consumed = true
		d.state = ""
		d.verifier = ""
		d.mu.Unlock()
		return GoogleCredential{}, videos.FailureCode("oauth_session_expired")
	}
	code, denied, err := g.callback(callbackURL, d.state)
	if err != nil {
		d.mu.Unlock()
		return GoogleCredential{}, err
	}
	verifier := d.verifier
	d.consumed = true
	d.state = ""
	d.verifier = ""
	d.mu.Unlock()
	if denied {
		return GoogleCredential{}, videos.FailureCode("oauth_denied")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {g.config.ClientID}, "redirect_uri": {g.config.RedirectURI}, "code_verifier": {verifier}}
	if g.config.ClientSecret != "" {
		form.Set("client_secret", g.config.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return GoogleCredential{}, videos.FailureCode("oauth_exchange_uncertain")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return GoogleCredential{}, videos.FailureCode("oauth_exchange_uncertain")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			return GoogleCredential{}, videos.FailureCode("authentication_required")
		}
		return GoogleCredential{}, videos.FailureCode("oauth_exchange_uncertain")
	}
	current := GoogleCredential{ClientID: g.config.ClientID, ClientSecret: g.config.ClientSecret, Scope: g.config.Scope, ChannelID: g.config.ChannelID}
	grant, err := googleTokenResponse(resp, current, "oauth_exchange_uncertain", true)
	if err != nil {
		return GoogleCredential{}, err
	}
	if err = g.verifyChannel(ctx, grant); err != nil {
		return GoogleCredential{}, err
	}
	return grant, nil
}

func (g *GoogleOAuth) callback(raw, state string) (string, bool, error) {
	u, err := url.Parse(raw)
	expected, _ := url.Parse(g.config.RedirectURI)
	if err != nil || u.Scheme != expected.Scheme || u.Host != expected.Host || u.EscapedPath() != expected.EscapedPath() || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", false, videos.ErrInput
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", false, videos.ErrInput
	}
	for key, values := range q {
		if len(values) != 1 {
			return "", false, videos.ErrInput
		}
		switch key {
		case "code", "state", "error", "error_description", "error_uri", "authuser", "prompt":
		case "scope":
			if values[0] != g.config.Scope {
				return "", false, videos.FailureCode("permission_denied")
			}
		case "client_id":
			if values[0] != g.config.ClientID {
				return "", false, videos.FailureCode("oauth_session_invalid")
			}
		case "redirect_uri":
			if values[0] != g.config.RedirectURI {
				return "", false, videos.FailureCode("oauth_session_invalid")
			}
		case "iss":
			if values[0] != "https://accounts.google.com" {
				return "", false, videos.FailureCode("oauth_session_invalid")
			}
		default:
			return "", false, videos.ErrInput
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		return "", false, videos.FailureCode("oauth_session_invalid")
	}
	code, denied := q.Get("code"), q.Get("error") != ""
	if denied {
		if code != "" {
			return "", false, videos.ErrInput
		}
		return "", true, nil
	}
	if !safeToken(code) || q.Has("error") || q.Has("error_description") || q.Has("error_uri") {
		return "", false, videos.ErrInput
	}
	return code, false, nil
}

func (g *GoogleOAuth) verifyChannel(ctx context.Context, grant GoogleCredential) error {
	q := url.Values{"part": {"id"}, "mine": {"true"}, "maxResults": {"50"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.channelsURL+"?"+q.Encode(), nil)
	if err != nil {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+grant.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := g.http.Do(req)
	if err != nil {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	defer clear(raw)
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	var v struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Items == nil || len(v.Items) > 50 {
		return videos.FailureCode("oauth_channel_unavailable")
	}
	found := false
	seen := map[string]bool{}
	for _, item := range v.Items {
		if !youtube.ValidChannel(item.ID) || seen[item.ID] {
			return videos.FailureCode("oauth_channel_unavailable")
		}
		seen[item.ID] = true
		found = found || item.ID == g.config.ChannelID
	}
	if !found {
		return videos.FailureCode("resource_denied")
	}
	return nil
}

// Revoke performs one explicit Google revocation request, without retry. The
// owner must disable and stop issuing this grant locally before calling it.
// A successful response does not erase stored copies. Any failed/uncertain
// revocation keeps the grant disabled and requires owner-directed recovery or
// fresh consent, never automatic reuse of the old credential.
func (g *GoogleOAuth) Revoke(ctx context.Context, grant GoogleCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if grant.ClientID != g.config.ClientID || grant.Scope != g.config.Scope || grant.ChannelID != g.config.ChannelID {
		return videos.FailureCode("permission_denied")
	}
	token := grant.RefreshToken
	if token == "" {
		token = grant.AccessToken
	}
	if !safeToken(token) {
		return videos.ErrInput
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.revokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return videos.FailureCode("revocation_uncertain")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return videos.FailureCode("revocation_uncertain")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return videos.FailureCode("revocation_uncertain")
	}
	return nil
}
