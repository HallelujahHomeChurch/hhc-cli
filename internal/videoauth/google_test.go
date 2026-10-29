package videoauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

func TestGoogleReadOnlyRefreshAndRotatedTokens(t *testing.T) {
	calls := 0
	responseScope := youtube.ReadScope
	responseRefresh := "fixture-rotated"
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Method != "POST" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("scope") != youtube.ReadScope || r.Form.Get("refresh_token") != "fixture-refresh" || r.Form.Get("client_id") != "fixture-client" || r.Form.Get("client_secret") != "fixture-secret" {
			t.Error("wrong refresh contract")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "refresh_token": responseRefresh, "scope": responseScope, "token_type": "Bearer", "expires_in": 3600})
	}))
	defer s.Close()
	r := NewGoogleRefresh()
	r.endpoint = s.URL
	r.http.Transport = s.Client().Transport
	credential := GoogleCredential{ClientID: "fixture-client", ClientSecret: "fixture-secret", RefreshToken: "fixture-refresh", Scope: youtube.ReadScope}
	next, err := r.Refresh(context.Background(), credential)
	if err != nil || next.RefreshToken != "fixture-rotated" || next.AccessToken != "fixture-access" || next.ExpiresAt.IsZero() {
		t.Fatal("valid refresh failed", err)
	}
	responseScope = ""
	responseRefresh = ""
	next, err = r.Refresh(context.Background(), credential)
	if err != nil || next.Scope != youtube.ReadScope || next.RefreshToken != "fixture-refresh" {
		t.Fatal("unchanged optional values failed", err)
	}
	responseScope = "https://www.googleapis.com/auth/youtube.upload"
	if next, err = r.Refresh(context.Background(), credential); err == nil || next.AccessToken != "" {
		t.Fatal("scope escalation accepted")
	}
	credential.Scope = "youtube.upload"
	if _, err = r.Refresh(context.Background(), credential); err == nil || calls != 3 {
		t.Fatal("invalid scope reached upstream")
	}
}

func TestGoogleRefreshNoRetryRedirectOrRawError(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			redirected := 0
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
			defer target.Close()
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				w.Write([]byte("SECRET provider error"))
			}))
			defer s.Close()
			r := NewGoogleRefresh()
			r.endpoint = s.URL
			r.http.Transport = s.Client().Transport
			next, err := r.Refresh(context.Background(), GoogleCredential{ClientID: "fixture-client", RefreshToken: "fixture-refresh", Scope: youtube.ReadScope})
			if err == nil || calls != 1 || redirected != 0 || next.AccessToken != "" || strings.Contains(err.Error(), "SECRET") || videos.SafeFailure(err).Retryable {
				t.Fatal("unsafe refresh error", err)
			}
		})
	}
}

func TestGoogleRefreshMalformedOrAmbiguousResponse(t *testing.T) {
	for _, body := range []string{`null`, `{"access_token":"SECRET"}`, `{"access_token":"SECRET","access_token":"other"}`, strings.Repeat(" ", 16385)} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		r := NewGoogleRefresh()
		r.endpoint = s.URL
		r.http.Transport = s.Client().Transport
		next, err := r.Refresh(context.Background(), GoogleCredential{ClientID: "fixture-client", RefreshToken: "fixture-refresh", Scope: youtube.ReadScope})
		s.Close()
		if err == nil || next.RefreshToken != "" || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("ambiguous result exposed credential")
		}
	}
}
