package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryruntime"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

type identityFixture struct {
	revoked atomic.Bool
	queries atomic.Int32
	ready   atomic.Bool
}

func (f *identityFixture) Authorize(_ context.Context, token, tool string) (queryruntime.Binding, error) {
	if token != "fixture-caller-a" && token != "fixture-caller-b" || f.revoked.Load() {
		return queryruntime.Binding{}, videos.FailureCode("authentication_required")
	}
	return queryruntime.Binding{CallerID: token, BindingID: tool}, nil
}
func (f *identityFixture) Query(_ context.Context, b queryruntime.Binding, tool string, in videos.Input) (videos.Result, error) {
	f.queries.Add(1)
	return videos.Result{Sources: []videos.SourceResult{{Source: "hhc", Page: videos.Page{Items: []videos.Video{{ID: "hhc:fixture", Title: b.CallerID}}, Complete: true}}}}, nil
}
func (f *identityFixture) Ready(context.Context) error {
	if !f.ready.Load() {
		return videos.FailureCode("broker_unavailable")
	}
	return nil
}
func request(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://query.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

const call = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_videos","arguments":{"source":"hhc"}}}`

func TestHTTPCallerIsolationRevocationAndRedactedAudit(t *testing.T) {
	f := &identityFixture{}
	s := NewServer(f, []string{"query.test"}, nil, "fixture")
	audits := []Audit{}
	s.Audit = func(a Audit) { audits = append(audits, a) }
	for _, caller := range []string{"fixture-caller-a", "fixture-caller-b"} {
		w := request(s, "POST", "/mcp", call, caller)
		if w.Code != 200 || !strings.Contains(w.Body.String(), caller) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(audits) != 2 || audits[0].CallerHash == audits[1].CallerHash || strings.Contains(fmt.Sprint(audits), "fixture-caller") {
		t.Fatal(audits)
	}
	f.revoked.Store(true)
	w := request(s, "POST", "/mcp", call, "fixture-caller-a")
	if w.Code != 401 || f.queries.Load() != 2 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatal(w.Code, f.queries.Load())
	}
}
func TestHTTPBoundariesAndTwoInstanceDrain(t *testing.T) {
	f := &identityFixture{}
	f.ready.Store(true)
	a := NewServer(f, []string{"query.test"}, nil, "fixture")
	b := NewServer(f, []string{"query.test"}, nil, "fixture")
	if request(a, "GET", "/health/ready", "", "").Code != 200 {
		t.Fatal("ready")
	}
	a.Drain()
	if request(a, "GET", "/health/ready", "", "").Code != 503 || request(a, "POST", "/mcp", call, "fixture-caller-a").Code != 503 || request(b, "POST", "/mcp", call, "fixture-caller-a").Code != 200 {
		t.Fatal("independent instance drain")
	}
	f.ready.Store(false)
	if request(b, "GET", "/health/ready", "", "").Code != 503 || request(b, "GET", "/health/live", "", "").Code != 200 {
		t.Fatal("readiness/liveness failure isolation")
	}
	for _, tc := range []struct {
		method, path, body, token string
		want                      int
	}{{"GET", "/mcp", "", "", 405}, {"POST", "/mcp", call, "", 401}, {"POST", "/mcp", "[" + call + "]", "fixture-caller-a", 400}, {"POST", "/mcp", strings.Repeat("x", 65537), "fixture-caller-a", 413}, {"POST", "/unknown", call, "fixture-caller-a", 404}, {"POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "fixture-caller-a", 202}} {
		if w := request(b, tc.method, tc.path, tc.body, tc.token); w.Code != tc.want {
			t.Errorf("%s %s: %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
	for _, header := range []string{"Origin", "Host", "Accept", "Content-Type", "MCP-Protocol-Version"} {
		r := httptest.NewRequest("POST", "http://query.test/mcp", strings.NewReader(call))
		r.Header.Set("Authorization", "Bearer fixture-caller-a")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Content-Type", "application/json")
		if header == "Host" {
			r.Host = "evil.test"
		} else {
			r.Header.Set(header, "invalid")
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Error(header, "accepted")
		}
	}
}
func TestProtocolToolAllowlistAndStdioParity(t *testing.T) {
	var calls int
	query := func(_ context.Context, tool string, in videos.Input) (videos.Result, error) {
		calls++
		return videos.Result{Sources: []videos.SourceResult{}, Incomplete: true}, nil
	}
	for _, raw := range []string{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{}}}`, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_videos","arguments":{"url":"https://bad"}}}`} {
		r, e := Decode([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		if Handle(context.Background(), r, query, "fixture").Error == nil {
			t.Fatal("unsafe tool accepted")
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
	r, _ := Decode([]byte(call))
	expected := Handle(context.Background(), r, query, "fixture")
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+call+"\n"), &out, query, "fixture"); err != nil {
		t.Fatal(err)
	}
	var actual Response
	if json.Unmarshal(out.Bytes(), &actual) != nil {
		t.Fatal(out.String())
	}
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(actual)
	var wantValue, gotValue any
	json.Unmarshal(want, &wantValue)
	json.Unmarshal(got, &gotValue)
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("stdio mismatch %s %s", want, got)
	}
	list, _ := Decode([]byte(`{"jsonrpc":"2.0","id":"tools","method":"tools/list"}`))
	raw, _ := json.Marshal(Handle(context.Background(), list, query, "fixture"))
	if !strings.Contains(string(raw), `"readOnlyHint":true`) || strings.Contains(string(raw), "upload") {
		t.Fatal(string(raw))
	}
}
func TestHTTPAdmissionLimit(t *testing.T) {
	s := NewServer(&identityFixture{}, []string{"query.test"}, nil, "fixture")
	for i := 0; i < cap(s.active); i++ {
		s.active <- struct{}{}
	}
	if request(s, http.MethodPost, "/mcp", call, "fixture-caller-a").Code != 503 {
		t.Fatal("admission not bounded")
	}
}

func TestCancellationNotificationPolicyOperation(t *testing.T) {
	r, err := Decode([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`))
	if err != nil || Operation(r) != "notifications/cancelled" {
		t.Fatalf("%s %v", Operation(r), err)
	}
}

func fixtureOAuthConfig() OAuthConfig {
	return OAuthConfig{ResourceURL: "https://query.test/mcp", AuthorizationServers: []string{"https://issuer.example"}, ScopesSupported: []string{"videos:read"}}
}
func TestProtectedResourceDiscoveryAndChallenges(t *testing.T) {
	config := fixtureOAuthConfig()
	server, err := NewServerWithOAuth(&identityFixture{}, []string{"query.test"}, nil, "fixture", config)
	if err != nil {
		t.Fatal(err)
	}
	// Constructor retains immutable configuration, not a caller-owned slice.
	config.AuthorizationServers[0] = "https://mutated.example"
	config.ScopesSupported[0] = "unapproved"
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		response := request(server, http.MethodGet, path, "", "")
		var metadata protectedResourceMetadata
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &metadata) != nil || metadata.Resource != "https://query.test/mcp" || len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != "https://issuer.example" || !reflect.DeepEqual(metadata.ScopesSupported, []string{"videos:read"}) || !reflect.DeepEqual(metadata.BearerMethodsSupported, []string{"header"}) {
			t.Fatalf("discovery response: %d %s", response.Code, response.Body.String())
		}
		if head := request(server, http.MethodHead, path, "", ""); head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal("metadata HEAD", head.Code)
		}
		if post := request(server, http.MethodPost, path, "", ""); post.Code != 405 {
			t.Fatal("metadata mutation accepted")
		}
	}
	response := request(server, http.MethodPost, "/mcp", call, "")
	challenge := response.Header().Get("WWW-Authenticate")
	if response.Code != 401 || !strings.Contains(challenge, `resource_metadata="https://query.test/.well-known/oauth-protected-resource/mcp"`) || !strings.Contains(challenge, `scope="videos:read"`) {
		t.Fatalf("no discovery challenge: %d %s", response.Code, challenge)
	}
	response = request(server, http.MethodPost, "/mcp", call, "invalid")
	if response.Code != 401 || !strings.Contains(response.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatal("invalid token challenge", response.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "http://query.test/mcp", strings.NewReader(call))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "bearer fixture-caller-a")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("case-insensitive scheme rejected: %d", w.Code)
	}
}
func TestOAuthDiscoveryConfigurationFailsClosed(t *testing.T) {
	for _, mutate := range []func(*OAuthConfig){func(c *OAuthConfig) { c.ResourceURL = "http://query.test/mcp" }, func(c *OAuthConfig) { c.ResourceURL = "https://query.test/other" }, func(c *OAuthConfig) { c.ResourceURL = "https://unaccepted.example/mcp" }, func(c *OAuthConfig) { c.ResourceURL = "https://user:fixture@query.test/mcp" }, func(c *OAuthConfig) { c.ResourceURL = "https://query.test/mcp?injected=1" }, func(c *OAuthConfig) { c.AuthorizationServers = nil }, func(c *OAuthConfig) { c.AuthorizationServers = []string{"http://issuer.example"} }, func(c *OAuthConfig) { c.ScopesSupported = []string{`read",injected="value`} }, func(c *OAuthConfig) { c.ScopesSupported = []string{"read write"} }, func(c *OAuthConfig) { c.ScopesSupported = nil }} {
		config := fixtureOAuthConfig()
		mutate(&config)
		if _, err := NewServerWithOAuth(&identityFixture{}, []string{"query.test"}, nil, "fixture", config); err == nil {
			t.Fatal("unsafe OAuth discovery config accepted")
		}
	}
}

type insufficientScopeIdentity struct{ identityFixture }

func (*insufficientScopeIdentity) Authorize(context.Context, string, string) (queryruntime.Binding, error) {
	return queryruntime.Binding{}, videos.FailureCode("insufficient_scope")
}
func TestInsufficientScopeChallenge(t *testing.T) {
	server, err := NewServerWithOAuth(&insufficientScopeIdentity{}, []string{"query.test"}, nil, "fixture", fixtureOAuthConfig())
	if err != nil {
		t.Fatal(err)
	}
	response := request(server, http.MethodPost, "/mcp", call, "fixture-caller-a")
	challenge := response.Header().Get("WWW-Authenticate")
	if response.Code != 403 || !strings.Contains(challenge, `error="insufficient_scope"`) || !strings.Contains(challenge, `scope="videos:read"`) {
		t.Fatalf("insufficient scope response: %d %s", response.Code, challenge)
	}
}

type revokedQueryIdentity struct{ identityFixture }

func (f *revokedQueryIdentity) Query(context.Context, queryruntime.Binding, string, videos.Input) (videos.Result, error) {
	return videos.Result{}, &videos.Failure{Code: "authentication_required", CallerAuthority: true}
}
func TestRevokedCallerDuringQueryReturns401WithoutData(t *testing.T) {
	s := NewServer(&revokedQueryIdentity{}, []string{"query.test"}, nil, "test")
	w := request(s, "POST", "/mcp", call, "fixture-caller-a")
	if w.Code != 401 || w.Body.Len() != 0 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "invalid_token") {
		t.Fatal("revoked caller response", w.Code, w.Body.String())
	}
}
