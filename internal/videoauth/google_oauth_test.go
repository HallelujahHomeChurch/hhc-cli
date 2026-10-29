package videoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

const oauthFixtureChannel = "UCabcdefghijklmnopqrstuv"

func oauthConfig() GoogleOAuthConfig {
	return GoogleOAuthConfig{ClientID: "fixture-client", ClientSecret: "fixture-client-SECRET", ClientType: "web", RedirectURI: "https://callback.example.invalid/youtube", ChannelID: oauthFixtureChannel}
}

func oauthClient(t *testing.T, handler http.HandlerFunc) (*GoogleOAuth, *httptest.Server) {
	t.Helper()
	g, err := NewGoogleOAuth(oauthConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewTLSServer(handler)
	g.http.Transport = s.Client().Transport
	g.tokenURL = s.URL + "/token"
	g.channelsURL = s.URL + "/channels"
	g.revokeURL = s.URL + "/revoke"
	t.Cleanup(s.Close)
	return g, s
}

func oauthCallback(s *GoogleSession, values url.Values) string {
	auth, _ := url.Parse(s.AuthorizationURL())
	values.Set("state", auth.Query().Get("state"))
	return s.data.owner.config.RedirectURI + "?" + values.Encode()
}

func validToken(w http.ResponseWriter, scope string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access-SECRET", "refresh_token": "fixture-refresh-SECRET", "token_type": "Bearer", "scope": scope, "expires_in": 3600})
}

func channelResponse(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]string{"id": id}}})
}

func TestGoogleOAuthPKCECodeBindingChannelAndRevocation(t *testing.T) {
	var challenge string
	tokenCalls, channelCalls, revokeCalls := 0, 0, 0
	g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			r.ParseForm()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Method != "POST" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != "fixture-client" || r.Form.Get("client_secret") != "fixture-client-SECRET" || r.Form.Get("redirect_uri") != oauthConfig().RedirectURI || r.Form.Get("code") != "fixture-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || len(r.Form.Get("code_verifier")) < 43 {
				t.Error("code/client/redirect/PKCE contract")
			}
			validToken(w, youtube.ReadScope)
		case "/channels":
			channelCalls++
			if r.Header.Get("Authorization") != "Bearer fixture-access-SECRET" || r.URL.Query().Get("mine") != "true" || r.URL.Query().Get("part") != "id" {
				t.Error("ownership verification contract")
			}
			channelResponse(w, oauthFixtureChannel)
		case "/revoke":
			revokeCalls++
			r.ParseForm()
			if r.Method != "POST" || r.Form.Get("token") != "fixture-refresh-SECRET" || r.URL.RawQuery != "" {
				t.Error("revocation secret transport")
			}
			w.WriteHeader(200)
		default:
			t.Error("unexpected URL")
		}
	})
	s, err := g.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(s.AuthorizationURL())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	challenge = q.Get("code_challenge")
	if u.Scheme != "https" || u.Host != "accounts.google.com" || q.Get("client_id") != "fixture-client" || q.Get("scope") != youtube.ReadScope || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 43 || challenge == "" || q.Has("code_verifier") || q.Get("access_type") != "offline" || q.Get("include_granted_scopes") != "false" || q.Get("redirect_uri") != oauthConfig().RedirectURI {
		t.Fatal("unsafe authorization URL")
	}
	callback := oauthCallback(s, url.Values{"code": {"fixture-code"}})
	grant, err := g.Complete(context.Background(), s, callback)
	if err != nil || grant.ChannelID != oauthFixtureChannel || grant.Scope != youtube.ReadScope || grant.RefreshToken != "fixture-refresh-SECRET" || tokenCalls != 1 || channelCalls != 1 || s.AuthorizationURL() != "" {
		t.Fatal("code completion failed", err)
	}
	if _, err = g.Complete(context.Background(), s, callback); err == nil || tokenCalls != 1 {
		t.Fatal("code replay accepted")
	}
	if err = g.Revoke(context.Background(), grant); err != nil || revokeCalls != 1 {
		t.Fatal("revocation failed", err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v %v %#v %v %#v", g, g, s, s, g.config, g.config), "SECRET") {
		t.Fatal("OAuth secrets formatted")
	}
	if strings.Contains(fmt.Sprintf("%#v", *s), "fixture-code") || strings.Contains(fmt.Sprintf("%#v", *s), "verifier") {
		t.Fatal("session value formatting exposed internals")
	}
}

func TestGoogleOAuthApprovedClientRedirectAndScope(t *testing.T) {
	for _, mutate := range []func(*GoogleOAuthConfig){
		func(c *GoogleOAuthConfig) { c.ClientType = "service" }, func(c *GoogleOAuthConfig) { c.ClientID = "" }, func(c *GoogleOAuthConfig) { c.ClientSecret = "" }, func(c *GoogleOAuthConfig) { c.Scope = "youtube.upload" }, func(c *GoogleOAuthConfig) {
			c.Scope = youtube.ReadScope + " https://www.googleapis.com/auth/youtube.force-ssl"
		}, func(c *GoogleOAuthConfig) { c.ChannelID = "wrong" }, func(c *GoogleOAuthConfig) { c.RedirectURI = "urn:ietf:wg:oauth:2.0:oob" }, func(c *GoogleOAuthConfig) { c.RedirectURI = "http://callback.example.invalid/youtube" }, func(c *GoogleOAuthConfig) { c.RedirectURI += "?query=1" }, func(c *GoogleOAuthConfig) { c.RedirectURI += "#fragment" }, func(c *GoogleOAuthConfig) { c.RedirectURI = "https://user@callback.example.invalid/youtube" },
	} {
		c := oauthConfig()
		mutate(&c)
		if _, err := NewGoogleOAuth(c); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	for _, redirect := range []string{"http://127.0.0.1:45678/callback", "http://[::1]:45678/callback"} {
		c := oauthConfig()
		c.ClientType = "desktop"
		c.ClientSecret = ""
		c.RedirectURI = redirect
		g, err := NewGoogleOAuth(c)
		if err != nil {
			t.Fatal("desktop loopback rejected", err)
		}
		if g.tokenURL != googleTokenURL || g.revokeURL != googleRevokeURL || g.channelsURL != googleChannelsURL {
			t.Fatal("production endpoints changed")
		}
	}
	c := oauthConfig()
	c.ClientType = "desktop"
	c.RedirectURI = "http://127.0.0.1/callback"
	if _, err := NewGoogleOAuth(c); err == nil {
		t.Fatal("nonexplicit loopback port")
	}
	c = oauthConfig()
	c.Scope = "https://www.googleapis.com/auth/youtube.force-ssl"
	g, err := NewGoogleOAuth(c)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := g.Begin(context.Background())
	u, _ := url.Parse(s.AuthorizationURL())
	if u.Query().Get("scope") != c.Scope {
		t.Fatal("explicit edit scope not selected")
	}
}

func TestGoogleOAuthCallbackValidationAndDenial(t *testing.T) {
	calls := 0
	g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; validToken(w, youtube.ReadScope) })
	mutations := []func(string) string{
		func(s string) string {
			return strings.Replace(s, "callback.example.invalid", "wrong.example.invalid", 1)
		},
		func(s string) string { return strings.Replace(s, "/youtube?", "/wrong?", 1) },
		func(s string) string { return s + "#fragment" }, func(s string) string { return s + "&state=duplicate" }, func(s string) string { return s + "&code=duplicate" }, func(s string) string { return s + "&unknown=SECRET" }, func(s string) string { return s + "&client_id=wrong" }, func(s string) string { return s + "&redirect_uri=https%3A%2F%2Fwrong.example.invalid%2Fcallback" }, func(s string) string { return s + "&iss=https%3A%2F%2Fwrong.example.invalid" }, func(s string) string { return s + "&error=access_denied" }, func(s string) string { return s + "&scope=youtube.upload" },
		func(s string) string {
			u, _ := url.Parse(s)
			q := u.Query()
			q.Del("state")
			u.RawQuery = q.Encode()
			return u.String()
		},
		func(s string) string {
			u, _ := url.Parse(s)
			q := u.Query()
			q.Del("code")
			u.RawQuery = q.Encode()
			return u.String()
		},
		func(s string) string {
			u, _ := url.Parse(s)
			q := u.Query()
			q.Set("state", "wrong")
			u.RawQuery = q.Encode()
			return u.String()
		},
	}
	for _, mutate := range mutations {
		s, _ := g.Begin(context.Background())
		callback := mutate(oauthCallback(s, url.Values{"code": {"fixture-code"}}))
		if grant, err := g.Complete(context.Background(), s, callback); err == nil || grant.AccessToken != "" || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid callback accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid callbacks reached exchange")
	}
	s, _ := g.Begin(context.Background())
	callback := oauthCallback(s, url.Values{"error": {"access_denied"}, "error_description": {"SECRET"}})
	if _, err := g.Complete(context.Background(), s, callback); videos.SafeFailure(err).Code != "oauth_denied" {
		t.Fatal(err)
	}
	if _, err := g.Complete(context.Background(), s, callback); err == nil || calls != 0 {
		t.Fatal("denial not consumed")
	}
	other, _ := NewGoogleOAuth(oauthConfig())
	s, _ = g.Begin(context.Background())
	if _, err := other.Complete(context.Background(), s, oauthCallback(s, url.Values{"code": {"fixture-code"}})); err == nil {
		t.Fatal("session crossed client instance")
	}
}

func TestGoogleOAuthExpiryAndOneShotConcurrency(t *testing.T) {
	var calls atomic.Int32
	g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			calls.Add(1)
			validToken(w, youtube.ReadScope)
		} else {
			channelResponse(w, oauthFixtureChannel)
		}
	})
	now := time.Now()
	g.now = func() time.Time { return now }
	s, _ := g.Begin(context.Background())
	callback := oauthCallback(s, url.Values{"code": {"fixture-code"}})
	now = now.Add(11 * time.Minute)
	if _, err := g.Complete(context.Background(), s, callback); err == nil || calls.Load() != 0 || s.AuthorizationURL() != "" {
		t.Fatal("expired session used")
	}
	g.now = time.Now
	s, _ = g.Begin(context.Background())
	callback = oauthCallback(s, url.Values{"code": {"fixture-code"}})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grant, err := g.Complete(context.Background(), s, callback)
			if err == nil && grant.AccessToken != "" {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatal("exchange not single use", calls.Load(), successes.Load())
	}
}

func TestGoogleOAuthTokenFailuresAreNotRetriedOrReplayed(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			g, srv := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://never-follow.example.invalid/token")
				w.WriteHeader(status)
				w.Write([]byte("SECRET"))
			})
			_ = srv
			s, _ := g.Begin(context.Background())
			callback := oauthCallback(s, url.Values{"code": {"fixture-code"}})
			grant, err := g.Complete(context.Background(), s, callback)
			if err == nil || calls != 1 || grant.AccessToken != "" || videos.SafeFailure(err).Retryable || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe exchange failure", err)
			}
			if _, err = g.Complete(context.Background(), s, callback); err == nil || calls != 1 {
				t.Fatal("ambiguous exchange replayed")
			}
		})
	}
}

func TestGoogleOAuthScopeChannelAndOfflineGrantFailures(t *testing.T) {
	for _, scenario := range []string{"scope", "channel", "missing-refresh", "bad-token-type", "channel-status"} {
		t.Run(scenario, func(t *testing.T) {
			g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/channels" {
					if scenario == "channel-status" {
						w.WriteHeader(403)
						return
					}
					id := oauthFixtureChannel
					if scenario == "channel" {
						id = "UC1234567890123456789012"
					}
					channelResponse(w, id)
					return
				}
				scope := youtube.ReadScope
				if scenario == "scope" {
					scope = "https://www.googleapis.com/auth/youtube.force-ssl"
				}
				if scenario == "missing-refresh" || scenario == "bad-token-type" {
					w.Header().Set("Content-Type", "application/json")
					typ := "Bearer"
					if scenario == "bad-token-type" {
						typ = "ID"
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "token_type": typ, "expires_in": 3600})
					return
				}
				validToken(w, scope)
			})
			s, _ := g.Begin(context.Background())
			if grant, err := g.Complete(context.Background(), s, oauthCallback(s, url.Values{"code": {"fixture-code"}})); err == nil || grant.AccessToken != "" {
				t.Fatal("unusable grant returned")
			}
		})
	}
}

func TestGoogleOAuthCancellationTimeoutAndRevocationFailure(t *testing.T) {
	var calls atomic.Int32
	g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		validToken(w, youtube.ReadScope)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s, _ := g.Begin(context.Background())
	callback := oauthCallback(s, url.Values{"code": {"fixture-code"}})
	if _, err := g.Complete(ctx, s, callback); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("cancelled exchange started", err)
	}
	g.http.Timeout = time.Millisecond
	if _, err := g.Complete(context.Background(), s, callback); err == nil {
		t.Fatal("timeout accepted")
	}
	if _, err := g.Complete(context.Background(), s, callback); err == nil {
		t.Fatal("timed-out session replayed")
	}
	grant := GoogleCredential{ClientID: g.config.ClientID, Scope: g.config.Scope, ChannelID: g.config.ChannelID, RefreshToken: "fixture-refresh-SECRET"}
	if err := g.Revoke(ctx, grant); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := g.Revoke(context.Background(), grant); err == nil || videos.SafeFailure(err).Retryable {
		t.Fatal("uncertain revocation succeeded or retryable")
	}
}

func TestGoogleOAuthEditScopeExchangeAndRefreshStayExplicit(t *testing.T) {
	edit := "https://www.googleapis.com/auth/youtube.force-ssl"
	g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels" {
			channelResponse(w, oauthFixtureChannel)
			return
		}
		validToken(w, edit)
	})
	g.config.Scope = edit
	s, _ := g.Begin(context.Background())
	grant, err := g.Complete(context.Background(), s, oauthCallback(s, url.Values{"code": {"fixture-code"}}))
	if err != nil || grant.Scope != edit {
		t.Fatal("explicit edit scope failed", err)
	}
	refresh := NewGoogleRefresh()
	refresh.endpoint = g.tokenURL
	refresh.http.Transport = g.http.Transport
	if grant, err = refresh.Refresh(context.Background(), grant); err != nil || grant.Scope != edit || grant.ChannelID != oauthFixtureChannel {
		t.Fatal("refresh changed edit binding", err)
	}
	c := descriptor()
	c.YouTube.Scope = edit
	if err = c.Validate(time.Now()); err != nil {
		t.Fatal("query cannot use approved edit scope", err)
	}
}

func TestGoogleOAuthRevocationNoRetryRedirectOrSecretError(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			g, _ := oauthClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://never-follow.example.invalid/revoke")
				w.WriteHeader(status)
				w.Write([]byte("SECRET upstream error"))
			})
			grant := GoogleCredential{ClientID: g.config.ClientID, Scope: g.config.Scope, ChannelID: g.config.ChannelID, RefreshToken: "fixture-refresh-SECRET"}
			err := g.Revoke(context.Background(), grant)
			if err == nil || calls.Load() != 1 || videos.SafeFailure(err).Retryable || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe revocation result", err)
			}
			grant.ClientID = "another-client"
			if err = g.Revoke(context.Background(), grant); err == nil || calls.Load() != 1 {
				t.Fatal("revocation crossed client")
			}
		})
	}
}

func TestGoogleOAuthSessionFreshnessAndZeroValue(t *testing.T) {
	g, err := NewGoogleOAuth(oauthConfig())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := g.Begin(context.Background())
	b, _ := g.Begin(context.Background())
	au, _ := url.Parse(a.AuthorizationURL())
	bu, _ := url.Parse(b.AuthorizationURL())
	if au.Query().Get("state") == bu.Query().Get("state") || au.Query().Get("code_challenge") == bu.Query().Get("code_challenge") {
		t.Fatal("session randomness reused")
	}
	state := au.Query().Get("state")
	if strings.Contains(fmt.Sprintf("%v %#v %v %#v", a, a, *a, *a), state) {
		t.Fatal("state formatted")
	}
	zero := new(GoogleSession)
	if zero.AuthorizationURL() != "" || !zero.ExpiresAt().IsZero() {
		t.Fatal("zero session usable")
	}
	if _, err := g.Complete(context.Background(), zero, "https://callback.example.invalid/youtube"); err == nil {
		t.Fatal("zero session accepted")
	}
}
