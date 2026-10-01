package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const humanAuthorizeEndpoint = "https://account.alive.org.tw/api/account/v1/oauth/authorize"

type HumanLoginOptions struct {
	NoInput  bool
	DeviceID string
	Scopes   []string
}

// HumanCredentials are in-memory until the profile layer durably saves the
// refresh credential. They are never JSON-visible or printable.
type HumanCredentials struct {
	access  Token
	refresh string
}

func (c HumanCredentials) AccessToken() Token   { return c.access }
func (c HumanCredentials) RefreshToken() string { return c.refresh }
func (HumanCredentials) String() string         { return "[redacted human credentials]" }
func (c HumanCredentials) GoString() string     { return c.String() }

type HumanClient struct {
	http                             *http.Client
	authorizeEndpoint, tokenEndpoint string
	openBrowser                      func(context.Context, string) error
}

func NewHumanClient() *HumanClient {
	return &HumanClient{http: newAuthHTTPClient(), authorizeEndpoint: humanAuthorizeEndpoint, tokenEndpoint: serviceTokenEndpoint, openBrowser: openLoginBrowser}
}

// Login only performs the explicit human PKCE flow. SA failures never invoke
// it. NoInput returns before acquiring a listener or opening a browser.
func (c *HumanClient) Login(ctx context.Context, options HumanLoginOptions) (HumanCredentials, error) {
	if err := ctx.Err(); err != nil {
		return HumanCredentials{}, err
	}
	if options.NoInput {
		return HumanCredentials{}, ErrAuthenticationRequired
	}
	scope, err := recordingScopes(options.Scopes)
	if err != nil || !validDeviceID(options.DeviceID) {
		return HumanCredentials{}, ErrInvalidAuthInput
	}
	scope += " offline_access"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return HumanCredentials{}, ErrAuthUnavailable
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	state, verifier := randomPKCEValue(), randomPKCEValue()
	challenge := sha256.Sum256([]byte(verifier))
	result := make(chan loginCallback, 1)
	var consumed atomic.Bool
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second,
		MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if r.Method != http.MethodGet || r.Host != listener.Addr().String() || r.URL.Path != "/oauth/callback" || r.URL.IsAbs() || len(r.URL.RawQuery) > 4096 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			q, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for key, values := range q {
				if len(values) != 1 || (key != "state" && key != "code" && key != "error" && key != "error_description" && key != "error_uri" && key != "iss") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			if q.Has("iss") && q.Get("iss") != "https://account.alive.org.tw" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			code, failure := q.Get("code"), q.Get("error")
			if (code == "") == (failure == "") || len(code) > 2048 || code != "" && !bearerPattern.MatchString(code) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !consumed.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusGone)
				return
			}
			callback := loginCallback{code: code}
			if failure != "" {
				callback.err = ErrAuthenticationRequired
				if failure == "access_denied" {
					callback.err = ErrPermissionDenied
				}
			}
			result <- callback
			io.WriteString(w, "登入授權已收到，請回到 HHC CLI 確認結果。")
		}),
	}
	defer server.Close()
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()
	authorize, err := url.Parse(c.authorizeEndpoint)
	if err != nil {
		return HumanCredentials{}, ErrAuthUnavailable
	}
	authorize.RawQuery = url.Values{
		"client_id": {"hhc-cli"}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {scope},
		"state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}.Encode()
	launched := make(chan error, 1)
	go func() { launched <- c.openBrowser(ctx, authorize.String()) }()
	select {
	case <-ctx.Done():
		return HumanCredentials{}, ctx.Err()
	case <-serveError:
		return HumanCredentials{}, ErrAuthUnavailable
	case err := <-launched:
		if err != nil {
			if ctx.Err() != nil {
				return HumanCredentials{}, ctx.Err()
			}
			return HumanCredentials{}, ErrAuthUnavailable
		}
	}
	select {
	case <-ctx.Done():
		return HumanCredentials{}, ctx.Err()
	case <-serveError:
		return HumanCredentials{}, ErrAuthUnavailable
	case callback := <-result:
		if callback.err != nil {
			return HumanCredentials{}, callback.err
		}
		return c.exchangeCode(ctx, callback.code, verifier, redirect, options.DeviceID, scope)
	}
}

type loginCallback struct {
	code string
	err  error
}

func randomPKCEValue() string {
	value := make([]byte, 32)
	// crypto/rand.Read cannot fail on supported Go/platform versions.
	rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

func validDeviceID(id string) bool {
	if len(id) < 32 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (c *HumanClient) exchangeCode(ctx context.Context, code, verifier, redirect, device, requestedScope string) (HumanCredentials, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"hhc-cli"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "device_id": {device}, "device_name": {"HHC CLI"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return HumanCredentials{}, ErrInvalidAuthInput
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return HumanCredentials{}, ctx.Err()
		}
		return HumanCredentials{}, ErrAuthUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		return HumanCredentials{}, ErrAuthenticationRequired
	}
	if response.StatusCode == 403 {
		return HumanCredentials{}, ErrPermissionDenied
	}
	if response.StatusCode != 200 && response.StatusCode != 400 {
		return HumanCredentials{}, ErrAuthUnavailable
	}
	wire, err := readOAuthResponse(response)
	if err != nil {
		if ctx.Err() != nil {
			return HumanCredentials{}, ctx.Err()
		}
		return HumanCredentials{}, err
	}
	if response.StatusCode == 400 {
		switch wire.Error {
		case "invalid_grant", "invalid_client":
			return HumanCredentials{}, ErrAuthenticationRequired
		case "invalid_scope":
			return HumanCredentials{}, ErrPermissionDenied
		default:
			return HumanCredentials{}, ErrInvalidAuthResponse
		}
	}
	allowed := map[string]bool{}
	for _, scope := range strings.Fields(requestedScope) {
		allowed[scope] = true
	}
	seen := map[string]bool{}
	actual := strings.Fields(wire.Scope)
	for _, scope := range actual {
		if !allowed[scope] || seen[scope] {
			return HumanCredentials{}, ErrInvalidAuthResponse
		}
		seen[scope] = true
	}
	if !seen["offline_access"] || len(actual) < 2 {
		return HumanCredentials{}, ErrPermissionDenied
	}
	if wire.TokenType != "Bearer" || len(wire.AccessToken) > 16384 || !bearerPattern.MatchString(wire.AccessToken) || len(wire.RefreshToken) > 4096 || !bearerPattern.MatchString(wire.RefreshToken) || wire.ExpiresIn <= 0 || wire.ExpiresIn > 86400 || wire.IDToken != "" || wire.Error != "" || len(response.Header.Values("Set-Cookie")) > 0 {
		return HumanCredentials{}, ErrInvalidAuthResponse
	}
	if !oauthResponseNoStore(response) {
		return HumanCredentials{}, ErrInvalidAuthResponse
	}
	expires := started.Add(time.Duration(wire.ExpiresIn)*time.Second - 15*time.Second)
	if !time.Now().Before(expires) {
		return HumanCredentials{}, ErrAuthenticationRequired
	}
	return HumanCredentials{access: Token{value: wire.AccessToken, expiresAt: expires, scope: strings.Join(actual, " ")}, refresh: wire.RefreshToken}, nil
}
