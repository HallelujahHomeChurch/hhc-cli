package querybroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryruntime"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFreshIntrospectionAudienceScopeAndRevocation(t *testing.T) {
	active := true
	aud := fixtureResource
	scope := "video.query.read"
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		u, p, ok := r.BasicAuth()
		if !ok || u != "fixture-client" || p != "fixture-secret" {
			t.Error("introspection credentials")
		}
		r.ParseForm()
		if r.Form.Get("token") != "fixture-incoming-token" || r.Form.Get("token_type_hint") != "access_token" {
			t.Error("wrong token request")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"active": active, "iss": "https://issuer.example.invalid", "sub": "fixture-subject", "aud": aud, "exp": time.Now().Add(time.Minute).Unix(), "scope": scope, "token_type": "Bearer"})
	}))
	defer server.Close()
	auth, err := NewIntrospector("https://issuer.example.invalid", server.URL, fixtureResource, "video.query.read", "fixture-client", "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth.http.Transport = server.Client().Transport
	if _, err = auth.Authenticate(context.Background(), "fixture-incoming-token"); err != nil {
		t.Fatal(err)
	}
	active = false
	if _, err = auth.Authenticate(context.Background(), "fixture-incoming-token"); err != ErrReauthorize {
		t.Fatal(err)
	}
	active = true
	aud = "https://wrong.example.invalid/mcp"
	if _, err = auth.Authenticate(context.Background(), "fixture-incoming-token"); err != ErrReauthorize {
		t.Fatal(err)
	}
	aud = fixtureResource
	scope = "other"
	if _, err = auth.Authenticate(context.Background(), "fixture-incoming-token"); err != ErrScope {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("unexpected cached authorization", calls)
	}
}
func TestGoogleReadOnlyRefreshNoRetryOrEscalation(t *testing.T) {
	calls := 0
	escalate := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("scope") != youtube.ReadScope || r.Form.Get("refresh_token") != "fixture-refresh" {
			t.Error("wrong refresh contract")
		}
		w.Header().Set("Content-Type", "application/json")
		scope := youtube.ReadScope
		if escalate {
			scope = "https://www.googleapis.com/auth/youtube.upload"
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-rotated", "scope": scope, "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	refresh := NewGoogleRefresh()
	refresh.endpoint = server.URL
	refresh.http.Transport = server.Client().Transport
	g := Grant{Source: "youtube", Kind: "oauth", Scope: youtube.ReadScope, Secrets: Secrets{ClientID: "fixture-client", RefreshToken: "fixture-refresh"}}
	next, _, err := refresh.Refresh(context.Background(), g)
	if err != nil || next.RefreshToken != "fixture-rotated" {
		t.Fatal(err)
	}
	escalate = true
	if _, _, err = refresh.Refresh(context.Background(), g); err != ErrReauthorize {
		t.Fatal(err)
	}
	g.Source = "hhc"
	if _, _, err = refresh.Refresh(context.Background(), g); err != ErrReauthorize {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestRuntimeSecretInjectionAndDatabaseFailClosed(t *testing.T) {
	for _, key := range []string{"HHC_QUERY_SECRET_FD", "HHC_QUERY_RUNTIME_SECRET"} {
		t.Setenv(key, "")
	}
	if _, err := LoadRuntimeSecrets(); err == nil {
		t.Fatal("missing injection accepted")
	}
	t.Setenv("HHC_QUERY_RUNTIME_SECRET", `{"databaseUrl":"fixture","masterKey":"fixture","introspectionSecret":"fixture"}`)
	if _, err := LoadRuntimeSecrets(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HHC_QUERY_RUNTIME_SECRET") != "" {
		t.Fatal("injected environment retained")
	}
	for _, raw := range []string{"postgres://user:pass@host/db?sslmode=disable", "postgres://user@host/db?sslmode=verify-full", "postgres://user:pass@host/db?sslmode=verify-full&service=secret", "postgres://user:pass@host/db?sslmode=verify-full&sslkey=/private/key"} {
		if db, err := OpenDatabase(raw); err == nil {
			db.Close()
			t.Fatal("unsafe database config")
		}
	}
	db, err := OpenDatabase("postgres://fixture:fixture@database.example.invalid/fixture?sslmode=verify-full")
	if err != nil {
		t.Fatal("valid explicit TLS configuration", err)
	}
	db.Close()
	if strings.Contains(fmt.Sprintf("%#v", RuntimeSecrets{MasterKey: "SECRET"}), "SECRET") {
		t.Fatal("secret formatting")
	}
}
func TestBackendBindingCannotCrossCallerOrPolicy(t *testing.T) {
	s := testStore(t)
	principal := Principal{Issuer: "https://issuer.example.invalid", Subject: "caller-a", Audience: fixtureResource, ExpiresAt: time.Now().Add(time.Hour)}
	p := seedPolicy(t, s, "d")
	id := CallerID(principal)
	if _, err := s.DB.Exec(`UPDATE hhc_query.caller_policy SET caller_id=$1 WHERE caller_id=$2`, id, p.CallerID); err != nil {
		t.Fatal(err)
	}
	auth := authFunc(func(_ context.Context, token string) (Principal, error) {
		if token != "fixture-incoming-token" {
			return Principal{}, ErrReauthorize
		}
		return principal, nil
	})
	backend := &Backend{Auth: auth, Store: s, Vault: s.Vault}
	binding, err := backend.Authorize(context.Background(), "fixture-incoming-token", "search_videos")
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.Permit(context.Background(), binding, "fixture-incoming-token", "hhc", "content:videos", "authorization", 0); err != nil {
		t.Fatal(err)
	}
	if err = backend.Permit(context.Background(), binding, "different-token-value", "hhc", "content:videos", "authorization", 0); err == nil {
		t.Fatal("binding replay with another caller")
	}
	binding.BindingID += "x"
	if err = backend.Permit(context.Background(), binding, "fixture-incoming-token", "hhc", "content:videos", "authorization", 0); err == nil {
		t.Fatal("forged binding")
	}
	runtime := queryruntime.NewEmbedded(backend)
	bound, err := runtime.Authorize(context.Background(), "fixture-incoming-token", "search_videos")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", bound), "fixture-incoming-token") {
		t.Fatal("binding formatting exposed caller bearer")
	}
	if _, err = s.DB.Exec(`UPDATE hhc_query.caller_policy SET revision=revision+1 WHERE caller_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = backend.Permit(context.Background(), bound, "fixture-incoming-token", "hhc", "content:videos", "authorization", 0); err == nil {
		t.Fatal("old policy binding survived revision")
	}
	if _, err = NewVault(bytes.Repeat([]byte{1}, 31), fixtureResource); err == nil {
		t.Fatal("invalid master key accepted")
	}
}

type authFunc func(context.Context, string) (Principal, error)

func (f authFunc) Authenticate(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}
