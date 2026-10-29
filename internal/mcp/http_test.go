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
