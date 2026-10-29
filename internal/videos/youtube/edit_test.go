package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

type editFixture struct {
	mu                                             sync.Mutex
	current                                        metadataSnippet
	etag, channel                                  string
	gets, puts                                     int
	putStatus                                      int
	failedReadback, extraProperty, missingCategory bool
	dropAfterWrite                                 bool
	body                                           map[string]json.RawMessage
	match                                          string
}

func newEditFixture(t *testing.T) (*Provider, *editFixture) {
	t.Helper()
	f := &editFixture{etag: "version-one", channel: fixtureChannel, current: metadataSnippet{Title: "Original", Description: "Keep description", Tags: []string{"old", "two words"}, CategoryID: "22", DefaultLanguage: "zh-Hant", DefaultAudioLanguage: "zh"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/youtube/v3/videos" || r.URL.Query().Get("part") != "snippet" || r.Header.Get("Authorization") != "Bearer fixture-edit" {
			t.Error("unexpected edit request")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			f.gets++
			if f.failedReadback && f.puts > 0 {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(503)
				return
			}
			raw, _ := json.Marshal(f.current)
			snippet := map[string]any{}
			json.Unmarshal(raw, &snippet)
			snippet["channelId"] = f.channel
			snippet["thumbnails"] = map[string]any{"default": map[string]string{"url": "https://example.invalid/thumbnail"}}
			if f.extraProperty {
				snippet["newMutableField"] = "preserve-or-stop"
			}
			if f.missingCategory {
				delete(snippet, "categoryId")
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": "abcdefghijk", "etag": f.etag, "snippet": snippet}}})
			return
		}
		if r.Method != http.MethodPut {
			t.Error("unexpected mutation method")
			w.WriteHeader(405)
			return
		}
		f.puts++
		f.match = r.Header.Get("If-Match")
		if json.NewDecoder(r.Body).Decode(&f.body) != nil {
			t.Error("invalid mutation JSON")
		}
		if f.putStatus != 0 {
			w.Header().Set("Location", serverRedirect())
			w.WriteHeader(f.putStatus)
			return
		}
		if f.match != "\""+f.etag+"\"" {
			w.WriteHeader(412)
			return
		}
		if len(f.body) != 3 || f.body["snippet"] == nil || f.body["id"] == nil || f.body["etag"] == nil {
			t.Error("unapproved top-level mutation fields")
		}
		if json.Unmarshal(f.body["snippet"], &f.current) != nil {
			t.Error("invalid snippet")
		}
		f.etag = "version-two"
		if f.dropAfterWrite {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				conn.Close()
			}
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "abcdefghijk", "etag": f.etag})
	}))
	t.Cleanup(server.Close)
	return &Provider{Client: queryhttp.New(server.URL), Grant: Grant{Bearer: "fixture-edit", Scope: EditScope, ChannelID: fixtureChannel}}, f
}
func serverRedirect() string        { return "https://example.invalid/unapproved" }
func titlePatch(title string) Patch { return Patch{Title: &title} }

func TestEditDryRunPreservesSnippetAndConditionalWriteReadback(t *testing.T) {
	p, f := newEditFixture(t)
	plan, err := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed"))
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Field != "title" || plan.ID != "youtube:abcdefghijk" || len(plan.PlanHash) != 64 {
		t.Fatalf("%+v %v", plan, err)
	}
	f.mu.Lock()
	puts := f.puts
	original := f.current
	f.mu.Unlock()
	if puts != 0 {
		t.Fatal("dry run wrote")
	}
	result, err := p.ApplyEdit(context.Background(), "abcdefghijk", titlePatch("Changed"), plan.ETag, plan.PlanHash)
	if err != nil || result.Outcome != "verified" || !result.RequestedActionSatisfied || result.DryRun {
		t.Fatalf("%+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 1 || f.gets != 3 || f.match != "\"version-one\"" {
		t.Fatal("missing preview/preflight/conditional write/readback", f.puts, f.gets, f.match)
	}
	want := original
	want.Title = "Changed"
	if !reflect.DeepEqual(f.current, want) {
		t.Fatalf("unrelated fields changed: %+v", f.current)
	}
	var snippet map[string]any
	json.Unmarshal(f.body["snippet"], &snippet)
	for _, field := range []string{"status", "privacyStatus", "publishAt", "localizations", "recordingDetails"} {
		if f.body[field] != nil || snippet[field] != nil {
			t.Fatal("unapproved write field", field)
		}
	}
	if snippet["channelId"] != nil || snippet["thumbnails"] != nil {
		t.Fatal("read-only snippet echoed")
	}
}
func TestEditRequiresExactPreviewAndNoAutomaticMutationRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 412, 429, 500, 503, 307} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, f := newEditFixture(t)
			plan, err := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed"))
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.putStatus = status
			f.mu.Unlock()
			result, err := p.ApplyEdit(context.Background(), "abcdefghijk", titlePatch("Changed"), plan.ETag, plan.PlanHash)
			if err == nil || result.RequestedActionSatisfied {
				t.Fatal("failed write claimed success")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.puts != 1 {
				t.Fatal("write retried", f.puts)
			}
			if status >= 500 || status == 307 {
				if result.Outcome != "uncertain" || videos.SafeFailure(err).Retryable {
					t.Fatal("ambiguous write can be retried", result, err)
				}
			}
		})
	}
	p, f := newEditFixture(t)
	plan, _ := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed"))
	for _, fields := range [][2]string{{"different", plan.PlanHash}, {plan.ETag, strings.Repeat("0", 64)}} {
		_, err := p.ApplyEdit(context.Background(), "abcdefghijk", titlePatch("Changed"), fields[0], fields[1])
		if videos.SafeFailure(err).Code != "state_changed" {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 0 {
		t.Fatal("stale plan wrote")
	}
}
func TestEditStopsForOwnershipUnknownFieldsAndReadbackFailure(t *testing.T) {
	for _, kind := range []string{"channel", "unknown", "category", "readonly"} {
		t.Run(kind, func(t *testing.T) {
			p, f := newEditFixture(t)
			f.mu.Lock()
			switch kind {
			case "channel":
				f.channel = "UC1234567890123456789012"
			case "unknown":
				f.extraProperty = true
			case "category":
				f.missingCategory = true
			case "readonly":
				p.Grant.Scope = ReadScope
			}
			f.mu.Unlock()
			if _, err := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed")); err == nil {
				t.Fatal("unsafe preview accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.puts != 0 {
				t.Fatal("unsafe mutation")
			}
		})
	}
	p, f := newEditFixture(t)
	plan, _ := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed"))
	f.mu.Lock()
	f.failedReadback = true
	f.mu.Unlock()
	result, err := p.ApplyEdit(context.Background(), "abcdefghijk", titlePatch("Changed"), plan.ETag, plan.PlanHash)
	if videos.SafeFailure(err).Code != "mutation_uncertain" || result.RequestedActionSatisfied || result.Outcome != "uncertain" {
		t.Fatalf("%+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 1 {
		t.Fatal("readback failure caused resend")
	}
}
func TestMetadataPatchValidationAndExplicitClears(t *testing.T) {
	blank := ""
	tooLong := strings.Repeat("a", 101)
	description := strings.Repeat("a", 5001)
	badTags := []string{""}
	oversizedTags := []string{strings.Repeat("a", 501)}
	for _, patch := range []Patch{{}, {Title: &blank}, {Title: &tooLong}, {Description: &description}, {Tags: &badTags}, {Tags: &oversizedTags}} {
		if patch.Validate() == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	p, f := newEditFixture(t)
	tags := []string{}
	patch := Patch{Description: &blank, Tags: &tags}
	plan, err := p.PlanEdit(context.Background(), "abcdefghijk", patch)
	if err != nil || len(plan.Changes) != 2 {
		t.Fatal(err, plan)
	}
	result, err := p.ApplyEdit(context.Background(), "abcdefghijk", patch, plan.ETag, plan.PlanHash)
	if err != nil || !result.RequestedActionSatisfied {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current.Title != "Original" || f.current.Description != "" || len(f.current.Tags) != 0 || f.current.CategoryID != "22" {
		t.Fatal("clear changed unrelated values")
	}
}

func TestLostWriteResponseReconcilesWithoutAnotherPUT(t *testing.T) {
	p, f := newEditFixture(t)
	patch := titlePatch("Changed")
	plan, err := p.PlanEdit(context.Background(), "abcdefghijk", patch)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.dropAfterWrite = true
	f.mu.Unlock()
	result, err := p.ApplyEdit(context.Background(), "abcdefghijk", patch, plan.ETag, plan.PlanHash)
	if videos.SafeFailure(err).Code != "mutation_uncertain" || result.RequestedActionSatisfied || result.Outcome != "uncertain" {
		t.Fatalf("lost response not uncertain: %+v %v", result, err)
	}
	current, err := p.PlanEdit(context.Background(), "abcdefghijk", patch)
	if err != nil || len(current.Changes) != 0 {
		t.Fatalf("read-only reconciliation failed: %+v %v", current, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 1 || f.current.Title != "Changed" {
		t.Fatal("lost response replayed or write absent", f.puts)
	}
}

type editRoundTrip func(*http.Request) (*http.Response, error)

func (f editRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMetadataCancellationBeforeAndDuringWrite(t *testing.T) {
	p, f := newEditFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.PlanEdit(cancelled, "abcdefghijk", titlePatch("Changed")); !errors.Is(err, context.Canceled) {
		t.Fatal("preflight did not cancel", err)
	}
	f.mu.Lock()
	gets := f.gets
	f.mu.Unlock()
	if gets != 0 {
		t.Fatal("cancelled preflight sent request")
	}
	plan, err := p.PlanEdit(context.Background(), "abcdefghijk", titlePatch("Changed"))
	if err != nil {
		t.Fatal(err)
	}
	original := p.Client.HTTP.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	attempts := 0
	p.Client.HTTP.Transport = editRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			attempts++
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return original.RoundTrip(r)
	})
	ctx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	result, err := p.ApplyEdit(ctx, "abcdefghijk", titlePatch("Changed"), plan.ETag, plan.PlanHash)
	if videos.SafeFailure(err).Code != "mutation_uncertain" || result.RequestedActionSatisfied || attempts != 1 {
		t.Fatalf("cancelled write lost uncertainty: %+v %v attempts=%d", result, err, attempts)
	}
}

func TestMetadataEditAcceptsExplicitDeviceManageScope(t *testing.T) {
	p, _ := newEditFixture(t)
	p.Grant.Scope = ManageScope
	patch := titlePatch("Device edit")
	plan, err := p.PlanEdit(context.Background(), "abcdefghijk", patch)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.ApplyEdit(context.Background(), "abcdefghijk", patch, plan.ETag, plan.PlanHash)
	if err != nil || !result.RequestedActionSatisfied {
		t.Fatal(err, result)
	}
}
