package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrAuthenticationRequired = errors.New("authentication_required")
	ErrPermissionDenied       = errors.New("permission_denied")
	ErrAuthUnavailable        = errors.New("auth_unavailable")
	ErrInvalidAuthResponse    = errors.New("invalid_auth_response")
	ErrInvalidAuthInput       = errors.New("invalid_input")
	bearerPattern             = regexp.MustCompile(`^[a-zA-Z0-9._~+/-]+=*$`)
	principalIDPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

const serviceTokenEndpoint = "https://account.alive.org.tw/api/account/v1/oauth/token"

// Token is an in-memory bearer. It has no JSON-visible fields and deliberately
// redacts fmt output; Bearer is for the authenticated API transport only.
type Token struct {
	value     string
	expiresAt time.Time
	scope     string
	principal Principal
}

type Principal struct {
	Type                string    `json:"type"`
	ID                  string    `json:"id"`
	ClientID            string    `json:"client_id"`
	CredentialID        string    `json:"credential_id,omitempty"`
	CredentialExpiresAt time.Time `json:"credential_expires_at"`
}

func validPrincipal(p *Principal, kind, client string) bool {
	if p == nil || p.Type != kind || p.ClientID != client || !principalIDPattern.MatchString(p.ID) || p.ID == "00000000-0000-0000-0000-000000000000" || !p.CredentialExpiresAt.After(time.Now()) {
		return false
	}
	if kind == "human" {
		return p.CredentialID == ""
	}
	return kind == "service" && principalIDPattern.MatchString(p.CredentialID) && p.CredentialID != "00000000-0000-0000-0000-000000000000"
}

func (t Token) Bearer() string       { return t.value }
func (t Token) ExpiresAt() time.Time { return t.expiresAt }
func (t Token) Scope() string        { return t.scope }
func (t Token) Principal() Principal { return t.principal }
func (Token) String() string         { return "[redacted access token]" }
func (t Token) GoString() string     { return t.String() }

type ServiceClient struct {
	http     *http.Client
	endpoint string
}

func NewServiceClient() *ServiceClient {
	return &ServiceClient{http: newAuthHTTPClient(), endpoint: serviceTokenEndpoint}
}

func newAuthHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func recordingScopes(scopes []string) (string, error) {
	if len(scopes) == 0 || len(scopes) > 3 {
		return "", ErrInvalidAuthInput
	}
	scopes = slices.Clone(scopes)
	slices.Sort(scopes)
	for i, scope := range scopes {
		if scope != "cms:recordings:read" && scope != "cms:recordings:write" && scope != "cms:recordings:publish" {
			return "", ErrInvalidAuthInput
		}
		if i > 0 && scopes[i-1] == scope {
			return "", ErrInvalidAuthInput
		}
	}
	return strings.Join(scopes, " "), nil
}

// Exchange performs one explicit client-credentials request. It never renews
// interactively, changes identity, follows redirects, persists or logs secrets.
func (c *ServiceClient) Exchange(ctx context.Context, clientID, secret string, scopes []string) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	if len(clientID) == 0 || len(clientID) > 128 || secret == "" || len(secret) > 4096 || !utf8.ValidString(secret) || strings.ContainsAny(secret, "\r\n\x00") {
		return Token{}, ErrInvalidAuthInput
	}
	for _, r := range clientID {
		if r <= 32 || r >= 127 || r == ':' {
			return Token{}, ErrInvalidAuthInput
		}
	}
	scope, err := recordingScopes(scopes)
	if err != nil {
		return Token{}, err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {scope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, ErrInvalidAuthInput
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(clientID, secret)
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Token{}, ctx.Err()
		}
		return Token{}, ErrAuthUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return Token{}, ErrAuthenticationRequired
	}
	if response.StatusCode == http.StatusForbidden {
		return Token{}, ErrPermissionDenied
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusBadRequest {
		return Token{}, ErrAuthUnavailable
	}
	wire, err := readOAuthResponse(response)
	if err != nil {
		if ctx.Err() != nil {
			return Token{}, ctx.Err()
		}
		return Token{}, err
	}
	if response.StatusCode == http.StatusBadRequest {
		if wire.Error == "invalid_scope" {
			return Token{}, ErrPermissionDenied
		}
		return Token{}, ErrInvalidAuthResponse
	}
	actual, err := recordingScopes(strings.Fields(wire.Scope))
	if err != nil || actual != scope || wire.Error != "" || wire.TokenType != "Bearer" || len(wire.AccessToken) > 16384 || !bearerPattern.MatchString(wire.AccessToken) || wire.ExpiresIn <= 0 || wire.ExpiresIn > 600 || wire.RefreshToken != "" || wire.IDToken != "" || len(response.Header.Values("Set-Cookie")) > 0 {
		return Token{}, ErrInvalidAuthResponse
	}
	if !oauthResponseNoStore(response) {
		return Token{}, ErrInvalidAuthResponse
	}
	if !validPrincipal(wire.Principal, "service", clientID) {
		return Token{}, ErrInvalidAuthResponse
	}
	// Conservatively account for request latency and stop using the token before
	// server expiry. A credential about to expire needs rotation, not fallback.
	expires := started.Add(time.Duration(wire.ExpiresIn)*time.Second - 15*time.Second)
	if !time.Now().Before(expires) {
		return Token{}, ErrAuthenticationRequired
	}
	return Token{value: wire.AccessToken, expiresAt: expires, scope: actual, principal: *wire.Principal}, nil
}

type oauthResponse struct {
	AccessToken  string     `json:"access_token"`
	TokenType    string     `json:"token_type"`
	ExpiresIn    int64      `json:"expires_in"`
	Scope        string     `json:"scope"`
	RefreshToken string     `json:"refresh_token"`
	IDToken      string     `json:"id_token"`
	Error        string     `json:"error"`
	Principal    *Principal `json:"principal"`
}

func readOAuthResponse(response *http.Response) (oauthResponse, error) {
	var wire oauthResponse
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	trimmed := bytes.TrimSpace(body)
	mediaType, _, mimeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || len(body) > 65536 || !utf8.Valid(body) || len(trimmed) == 0 || trimmed[0] != '{' || mimeErr != nil || mediaType != "application/json" {
		return wire, ErrInvalidAuthResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&wire) != nil {
		return oauthResponse{}, ErrInvalidAuthResponse
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return oauthResponse{}, ErrInvalidAuthResponse
	}
	return wire, nil
}

func oauthResponseNoStore(response *http.Response) bool {
	for directive := range strings.SplitSeq(response.Header.Get("Cache-Control"), ",") {
		if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
			return true
		}
	}
	return false
}
