package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

const fixtureChannel = "UCabcdefghijklmnopqrstuv"

func TestSearchDetailsQuotaAndChannelIsolation(t *testing.T) {
	costs := []string{}
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Has("key") || r.Header.Get("X-Goog-Api-Key") != "fixture-key" || r.Header.Get("Authorization") != "" {
			t.Error("credential isolation")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/youtube/v3/search":
			if r.URL.Query().Get("channelId") != fixtureChannel || r.URL.Query().Get("type") != "video" {
				t.Error("resource selection")
			}
			fmt.Fprint(w, `{"items":[{"id":{"videoId":"abcdefghijk"}}],"nextPageToken":"next"}`)
		case "/youtube/v3/videos":
			fmt.Fprintf(w, `{"items":[{"id":"abcdefghijk","snippet":{"title":"Fixture","channelId":%q},"status":{"privacyStatus":"public"},"contentDetails":{"duration":"PT1M"}}]}`, fixtureChannel)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer s.Close()
	c := queryhttp.New(s.URL)
	c.Permit = func(_ context.Context, bucket string, cost int) error {
		costs = append(costs, fmt.Sprintf("%s:%d", bucket, cost))
		return nil
	}
	p := &Provider{Client: c, Grant: Grant{APIKey: "fixture-key", ChannelID: fixtureChannel}}
	in, _ := (videos.Input{Limit: 1}).Normalize()
	r, err := p.Search(context.Background(), in)
	if err != nil || r.Complete || len(r.Items) != 1 || r.Items[0].Duration != "PT1M" || requests != 2 || fmt.Sprint(costs) != "[youtube.search:1 youtube.default:1]" {
		t.Fatalf("%+v %v costs=%v", r, err, costs)
	}
	p.Grant.ChannelID = "UC1234567890123456789012"
	r, err = p.Get(context.Background(), "abcdefghijk", in)
	if err == nil || len(r.Items) != 0 || videos.SafeFailure(err).Code != "resource_denied" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestOAuthAndPrivateVisibility(t *testing.T) {
	var public atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !public.Load() && (r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Goog-Api-Key") != "") {
			t.Error("wrong credential")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"items":[{"id":"abcdefghijk","snippet":{"title":"Private","channelId":%q},"status":{"privacyStatus":"private"}}]}`, fixtureChannel)
	}))
	defer s.Close()
	p := &Provider{Client: queryhttp.New(s.URL), Grant: Grant{Bearer: "fixture", Scope: ReadScope, ChannelID: fixtureChannel}}
	in, _ := (videos.Input{}).Normalize()
	r, err := p.Get(context.Background(), "abcdefghijk", in)
	if err != nil || r.Items[0].Privacy != "private" {
		t.Fatalf("%+v %v", r, err)
	}
	p.Grant.Scope = "youtube.upload"
	if _, err = p.Get(context.Background(), "abcdefghijk", in); videos.SafeFailure(err).Code != "permission_denied" {
		t.Fatal(err)
	}
	p.Grant.Bearer = ""
	p.Grant.APIKey = "fixture-key"
	public.Store(true)
	// Details outside the approved public visibility are discarded.
	if _, err = p.Get(context.Background(), "abcdefghijk", in); videos.SafeFailure(err).Code != "resource_denied" {
		t.Fatal(err)
	}
}
func TestMalformedAndRepeatedPages(t *testing.T) {
	for _, body := range []string{`{"items":[{"id":{"videoId":"bad"}}]}`, `{"items":[],"nextPageToken":"again"}`} {
		t.Run(strings.Split(body, ":")[0]+fmt.Sprint(len(body)), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer s.Close()
			p := &Provider{Client: queryhttp.New(s.URL), Grant: Grant{APIKey: "fixture", ChannelID: fixtureChannel}}
			in, _ := (videos.Input{}).Normalize()
			if _, err := p.Search(context.Background(), in); videos.SafeFailure(err).Code != "invalid_response" {
				t.Fatal(err)
			}
		})
	}
}
