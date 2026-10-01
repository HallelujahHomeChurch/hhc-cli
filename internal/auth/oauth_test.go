package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopbackPKCEStateAndSingleExchange(t *testing.T) {
	var challenge, redirect, callbackBody string
	var exchanges atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("not public-client form exchange")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		v := r.PostForm
		if len(v) != 7 || v.Get("grant_type") != "authorization_code" || v.Get("client_id") != "hhc-cli" || v.Get("code") != "single-use-code" || v.Get("redirect_uri") != redirect || v.Get("device_id") != strings.Repeat("d", 43) || v.Get("device_name") != "HHC CLI" {
			t.Errorf("invalid exchange fields: %v", len(v))
		}
		hash := sha256.Sum256([]byte(v.Get("code_verifier")))
		if len(v.Get("code_verifier")) != 43 || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
			t.Error("PKCE challenge mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, `{"access_token":"human-bearer","token_type":"Bearer","expires_in":900,"scope":"cms:recordings:read offline_access","refresh_token":"human-refresh"}`)
	}))
	defer server.Close()
	client := NewHumanClient()
	client.http.Transport = server.Client().Transport
	client.tokenEndpoint = server.URL
	client.openBrowser = func(ctx context.Context, raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		if u.Scheme != "https" || u.Host != "account.alive.org.tw" || u.Path != "/api/account/v1/oauth/authorize" || q.Get("client_id") != "hhc-cli" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) != 43 || len(challenge) != 43 || q.Get("code_verifier") != "" || q.Get("scope") != "cms:recordings:read offline_access" {
			t.Error("invalid native authorization request")
		}
		cb, err := url.Parse(redirect)
		if err != nil {
			return err
		}
		if cb.Scheme != "http" || cb.Hostname() != "127.0.0.1" || cb.Port() == "" || cb.Path != "/oauth/callback" {
			t.Error("not registered loopback callback")
		}
		for _, invalid := range []url.Values{
			{"state": {"wrong"}, "code": {"single-use-code"}},
			{"state": {q.Get("state"), q.Get("state")}, "code": {"single-use-code"}},
			{"state": {q.Get("state")}, "code": {"single-use-code"}, "error": {"access_denied"}},
		} {
			cb.RawQuery = invalid.Encode()
			res, err := http.Get(cb.String())
			if err != nil {
				return err
			}
			res.Body.Close()
			if res.StatusCode != 400 {
				t.Errorf("accepted invalid callback: %d", res.StatusCode)
			}
		}
		cb.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"single-use-code"}}.Encode()
		res, err := http.Get(cb.String())
		if err != nil {
			return err
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return err
		}
		callbackBody = string(data)
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" || strings.Contains(callbackBody, "single-use-code") || strings.Contains(callbackBody, q.Get("state")) {
			t.Error("callback leaked credentials or falsely failed")
		}
		res, err = http.Get(cb.String())
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != 410 {
			t.Errorf("replayed callback: %d", res.StatusCode)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := client.Login(ctx, HumanLoginOptions{DeviceID: strings.Repeat("d", 43), Scopes: []string{"cms:recordings:read"}})
	if err != nil || got.AccessToken().Bearer() != "human-bearer" || got.RefreshToken() != "human-refresh" || got.AccessToken().Scope() != "cms:recordings:read offline_access" || exchanges.Load() != 1 {
		t.Fatalf("human exchange failed: %v count=%d", err, exchanges.Load())
	}
	data, err := json.Marshal(got)
	if err != nil || string(data) != "{}" || strings.Contains(fmt.Sprintf("%+v %#v", got, got), "human-refresh") {
		t.Fatal("credential output leak")
	}
	if res, err := http.Get(redirect); err == nil {
		res.Body.Close()
		t.Fatal("callback listener survived login")
	}
}

func TestNoInputNeverOpensBrowser(t *testing.T) {
	client := NewHumanClient()
	opened := false
	client.openBrowser = func(context.Context, string) error { opened = true; return nil }
	_, err := client.Login(context.Background(), HumanLoginOptions{NoInput: true, DeviceID: strings.Repeat("d", 43), Scopes: []string{"cms:recordings:read"}})
	if !errors.Is(err, ErrAuthenticationRequired) || opened {
		t.Fatalf("noninteractive opened browser: %v %t", err, opened)
	}
}

func TestHumanLoginCancellationClosesListenerWithoutExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewHumanClient()
	var redirect string
	client.openBrowser = func(_ context.Context, stringValue string) error {
		u, _ := url.Parse(stringValue)
		redirect = u.Query().Get("redirect_uri")
		cancel()
		return nil
	}
	if _, err := client.Login(ctx, HumanLoginOptions{DeviceID: strings.Repeat("d", 43), Scopes: []string{"cms:recordings:read"}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if redirect == "" {
		t.Fatal("listener never started")
	}
	if res, err := http.Get(redirect); err == nil {
		res.Body.Close()
		t.Fatal("cancelled listener survived")
	}
}

func TestHumanLoginCancellationDoesNotWaitForBrowserLauncher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewHumanClient()
	release := make(chan struct{})
	defer close(release)
	opened := make(chan string, 1)
	client.openBrowser = func(_ context.Context, target string) error {
		u, _ := url.Parse(target)
		opened <- u.Query().Get("redirect_uri")
		<-release
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.Login(ctx, HumanLoginOptions{DeviceID: strings.Repeat("d", 43), Scopes: []string{"cms:recordings:read"}})
		done <- err
	}()
	redirect := <-opened
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation waits for browser launcher")
	}
	if res, err := http.Get(redirect); err == nil {
		res.Body.Close()
		t.Fatal("cancelled listener survived")
	}
}

func TestHumanExchangeRejectsUntrustedResponsesWithoutRetry(t *testing.T) {
	valid := `{"access_token":"token","token_type":"Bearer","expires_in":900,"scope":"cms:recordings:read offline_access","refresh_token":"refresh"}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"scope escalation", 200, strings.Replace(valid, "cms:recordings:read", "cms:recordings:publish", 1), ErrInvalidAuthResponse},
		{"no recording permission", 200, strings.Replace(valid, "cms:recordings:read offline_access", "offline_access", 1), ErrPermissionDenied},
		{"invalid JSON", 200, valid + ` {}`, ErrInvalidAuthResponse},
		{"oversized", 200, strings.Repeat("x", 65537), ErrInvalidAuthResponse},
		{"unbounded expiry", 200, strings.Replace(valid, "900", "9223372036854775807", 1), ErrInvalidAuthResponse},
		{"missing refresh", 200, strings.Replace(valid, `"refresh_token":"refresh"`, `"refresh_token":""`, 1), ErrInvalidAuthResponse},
		{"unexpected ID token", 200, strings.Replace(valid, `"refresh_token":"refresh"`, `"refresh_token":"refresh","id_token":"unverified"`, 1), ErrInvalidAuthResponse},
		{"redirect", 302, `{"error_description":"secret"}`, ErrAuthUnavailable},
		{"denied", 403, `{"error_description":"secret"}`, ErrPermissionDenied},
		{"invalid grant", 400, `{"error":"invalid_grant","error_description":"secret"}`, ErrAuthenticationRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Location", "/leak-code")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := NewHumanClient()
			client.http.Transport = server.Client().Transport
			client.tokenEndpoint = server.URL
			got, err := client.exchangeCode(context.Background(), "code", "verifier", "http://127.0.0.1:12345/oauth/callback", strings.Repeat("d", 43), "cms:recordings:read offline_access")
			if !errors.Is(err, tc.want) || requests.Load() != 1 || got.RefreshToken() != "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("untrusted response accepted/retried/leaked: %v requests=%d", err, requests.Load())
			}
		})
	}
}
