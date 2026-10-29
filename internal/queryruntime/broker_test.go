package queryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/hhc"
)

const testChannel = "UCabcdefghijklmnopqrstuv"

func fixtureBroker(t *testing.T, h http.Handler) *Broker {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-socket cloud broker fixture")
	}
	dir, err := os.MkdirTemp("/tmp", "hhc-broker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: h}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	b, err := NewBroker(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.client.CloseIdleConnections() })
	return b
}
func binding(id string) Binding {
	var b Binding
	b.CallerID = id
	b.BindingID = "binding-" + id
	b.YouTube.Enabled = true
	b.YouTube.Access = "owner"
	b.YouTube.ChannelID = testChannel
	return b
}
func TestBrokerIsolationRevocationAndConcurrentTokenDelegation(t *testing.T) {
	var revoked atomic.Bool
	var exchanges atomic.Int32
	var once sync.Once
	var credentialCalls atomic.Int32
	b := fixtureBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/ready":
			fmt.Fprint(w, `{"ready":true}`)
		case "/v1/authorize":
			id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-token-")
			json.NewEncoder(w).Encode(binding(id))
		case "/v1/credential":
			var req credentialRequest
			json.NewDecoder(r.Body).Decode(&req)
			credentialCalls.Add(1)
			once.Do(func() { exchanges.Add(1) })
			if req.BindingID != "binding-"+req.CallerID {
				t.Error("binding mismatch")
			}
			json.NewEncoder(w).Encode(credential{CallerID: req.CallerID, BindingID: req.BindingID, Source: req.Source, Resource: req.Resource, Kind: "oauth", Scope: "https://www.googleapis.com/auth/youtube.readonly", Token: "fixture-" + req.CallerID, ExpiresAt: time.Now().Add(time.Hour)})
		case "/v1/permit":
			if revoked.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{}`)
			} else {
				fmt.Fprint(w, `{"allowed":true}`)
			}
		default:
			t.Error("unexpected broker operation", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	// Two independent runtime clients share the fixture coordinator. The runtime
	// itself performs no OAuth exchange/refresh and caches no credentials.
	b2 := &Broker{client: b.client, newUpstream: queryhttp.New}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := b
			if i%2 == 0 {
				client = b2
			}
			id := fmt.Sprintf("%02d", i)
			auth, err := client.Authorize(context.Background(), "fixture-token-"+id, "get_video")
			if err != nil {
				t.Error(err)
				return
			}
			v, err := client.token(context.Background(), auth, "youtube", testChannel)
			if err != nil || v.Token != "fixture-"+id {
				t.Errorf("isolated token failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if credentialCalls.Load() != 24 || exchanges.Load() != 1 {
		t.Fatal(credentialCalls.Load(), exchanges.Load())
	}
	if b.Ready(context.Background()) != nil {
		t.Fatal("fixture readiness")
	}
	revoked.Store(true)
	if err := b.permit(binding("one"), "youtube", testChannel)(context.Background(), "youtube.default", 1); videos.SafeFailure(err).Code != "permission_denied" {
		t.Fatal(err)
	}
}
func TestCredentialResponsesFailClosed(t *testing.T) {
	for _, mutate := range []string{"caller", "binding", "source", "resource", "expiry", "kind"} {
		t.Run(mutate, func(t *testing.T) {
			b := fixtureBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q credentialRequest
				json.NewDecoder(r.Body).Decode(&q)
				v := credential{CallerID: q.CallerID, BindingID: q.BindingID, Source: q.Source, Resource: q.Resource, Kind: "oauth", Scope: "https://www.googleapis.com/auth/youtube.readonly", Token: "fixture", ExpiresAt: time.Now().Add(time.Hour)}
				switch mutate {
				case "caller":
					v.CallerID = "other"
				case "binding":
					v.BindingID = "other"
				case "source":
					v.Source = "hhc"
				case "resource":
					v.Resource = "other"
				case "expiry":
					v.ExpiresAt = time.Now()
				case "kind":
					v.Kind = "human"
				}
				json.NewEncoder(w).Encode(v)
			}))
			result, err := b.Query(context.Background(), binding("one"), "get_video", videos.Input{ID: "youtube:abcdefghijk"})
			if err != nil || !result.Incomplete || len(result.Sources) != 1 || result.Sources[0].Error == nil || len(result.Sources[0].Items) != 0 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPostReadRevocationDiscardsData(t *testing.T) {
	var permits atomic.Int32
	b := fixtureBroker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/permit" {
			t.Error(r.URL.Path)
		}
		if permits.Add(1) > 1 {
			w.WriteHeader(403)
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{"allowed":true}`)
	}))
	b.newUpstream = func(base string) *queryhttp.Client {
		c := queryhttp.New(base)
		c.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "" || r.URL.Host != "www.alive.org.tw" {
				t.Error("public isolation")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: ioBody(`{"data":[{"id":"one","title":"Withdrawn","youtubeVideoId":"abcdefghijk"}],"meta":{"page":1,"pageSize":100,"total":1}}`)}, nil
		})
		return c
	}
	auth := Binding{CallerID: "one", BindingID: "binding-one"}
	auth.HHC.Enabled = true
	auth.HHC.Access = "public"
	result, err := b.Query(context.Background(), auth, "search_videos", videos.Input{Source: "hhc"})
	if err != nil || !result.Incomplete || len(result.Sources[0].Items) != 0 || result.Sources[0].Error == nil {
		t.Fatalf("%+v %v", result, err)
	}
}
func ioBody(s string) readCloser { return readCloser{strings.NewReader(s)} }

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }
func TestNoPlaintextFallbackOrRelativeBroker(t *testing.T) {
	if _, err := NewBroker("credentials.json"); err == nil {
		t.Fatal("relative broker accepted")
	}
	b, err := NewBroker(filepath.Join(t.TempDir(), "nonexistent-hhc-query-broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Ready(context.Background()) == nil {
		t.Fatal("missing broker ready")
	}
	if fmt.Sprintf("%#v", credential{Token: "SECRET"}) != "[redacted credential]" || strings.Contains(fmt.Sprintf("%#v", hhc.Grant{Bearer: "SECRET"}), "SECRET") {
		t.Fatal("secret formatting")
	}
}
