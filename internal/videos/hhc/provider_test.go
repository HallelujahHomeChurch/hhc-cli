package hhc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

func TestPublicPaginationFilterAndBound(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/videos" || r.URL.Query().Has("q") || r.Header.Get("Authorization") != "" {
			t.Error("public query isolation")
		}
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" {
			fmt.Fprint(w, `{"data":[{"id":"one","title":"Other","youtubeVideoId":"abcdefghijk"}],"meta":{"page":1,"pageSize":100,"total":2}}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"two","title":"聚會","youtubeVideoId":"12345678901"}],"meta":{"page":2,"pageSize":100,"total":2}}`)
		}
	}))
	defer s.Close()
	p := &Provider{Client: queryhttp.New(s.URL)}
	in, _ := (videos.Input{Query: "聚會", MaxPages: 1}).Normalize()
	r, err := p.Search(context.Background(), in)
	if err != nil || r.Complete || len(r.Items) != 0 || requests != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	in.MaxPages = 2
	r, err = p.Search(context.Background(), in)
	if err != nil || !r.Complete || len(r.Items) != 1 || r.Items[0].ID != "hhc:two" || r.Items[0].Publication != "published" || r.Scanned != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestHumanScopeAndPublishedSnapshotSafety(t *testing.T) {
	p := &Provider{Client: queryhttp.New("https://invalid.invalid"), Grant: Grant{Bearer: "fixture", Scope: "cms:pages:read"}}
	in, _ := (videos.Input{}).Normalize()
	if _, err := p.Search(context.Background(), in); videos.SafeFailure(err).Code != "permission_denied" {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/admin/content/videos" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("wrong admin contract")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"one","youtubeVideoId":"abcdefghijk","isPublished":true,"translations":[{"locale":"zh-Hant","title":"Draft title"}],"createdBy":"PRIVATE-MEMBER"}],"meta":{"page":1,"pageSize":100,"total":1}}`)
	}))
	defer s.Close()
	p.Client = queryhttp.New(s.URL)
	p.Grant.Human = true
	r, err := p.Search(context.Background(), in)
	raw, _ := json.Marshal(r)
	if err != nil || len(r.Items) != 1 || r.Items[0].Publication != "unknown" || strings.Contains(string(raw), "PRIVATE-MEMBER") {
		t.Fatalf("%+v %v", r, err)
	}
	// isPublished describes a published snapshot, not necessarily the current
	// draft's youtubeVideoId. Only the public projection can confirm that join.
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("human token leaked")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"public-one","title":"Published","youtubeVideoId":"abcdefghijk"}],"meta":{"page":1,"pageSize":100,"total":1}}`)
	}))
	defer public.Close()
	p.PublicClient = queryhttp.New(public.URL)
	in.YouTubeVideoID = "abcdefghijk"
	r, err = p.Search(context.Background(), in)
	if err != nil || r.Items[0].Provenance != "hhc.public" || r.Items[0].Publication != "published" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestInconsistentPaginationFailsClosed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"one","title":"Repeated"}],"meta":{"page":1,"pageSize":100,"total":200}}`)
	}))
	defer s.Close()
	in, _ := (videos.Input{}).Normalize()
	if _, err := (&Provider{Client: queryhttp.New(s.URL)}).Search(context.Background(), in); videos.SafeFailure(err).Code != "invalid_response" {
		t.Fatal(err)
	}
}

func TestWriteScopesRejected(t *testing.T) {
	p := &Provider{Grant: Grant{Human: true, Bearer: "fixture", Scope: "cms:pages:read cms:pages:write"}}
	if _, _, err := p.route(); videos.SafeFailure(err).Code != "permission_denied" {
		t.Fatal(err)
	}
}

func TestHumanGetUsesExactDetailRoute(t *testing.T) {
	const id = "known-after-page-bound"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/admin/content/videos/"+id || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("incorrect detail route: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"id":%q,"youtubeVideoId":"abcdefghijk","isPublished":false,"translations":[{"locale":"zh-Hant","title":"Known detail"}]},"error":null}`, id)
	}))
	defer server.Close()
	provider := &Provider{Client: queryhttp.New(server.URL), Grant: Grant{Human: true, Scope: "cms:pages:read", Bearer: "fixture"}}
	in, _ := (videos.Input{MaxPages: 1}).Normalize()
	page, err := provider.Get(context.Background(), id, in)
	if err != nil || calls != 1 || !page.Complete || page.Scanned != 1 || len(page.Items) != 1 || page.Items[0].ID != "hhc:"+id || page.Items[0].Title != "Known detail" || page.Coverage != "human_content" {
		t.Fatalf("detail lookup failed: %+v %v calls=%d", page, err, calls)
	}
}
func TestHumanGetRejectsMismatchedOrMissingDetails(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{{"mismatched", `{"data":{"id":"other","isPublished":false}}`, 200, "invalid_response"}, {"null", `{"data":null}`, 200, "invalid_response"}, {"envelope_error", `{"data":{"id":"wanted","isPublished":false},"error":{"message":"secret"}}`, 200, "invalid_response"}, {"missing", `{"error":{"message":"secret"}}`, 404, "not_found"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			provider := &Provider{Client: queryhttp.New(server.URL), Grant: Grant{Human: true, Scope: "cms:pages:read", Bearer: "fixture"}}
			in, _ := (videos.Input{}).Normalize()
			_, err := provider.Get(context.Background(), "wanted", in)
			if err == nil || videos.SafeFailure(err).Code != tc.code || strings.Contains(err.Error(), "secret") {
				t.Fatalf("detail failure not isolated: %v", err)
			}
		})
	}
}
