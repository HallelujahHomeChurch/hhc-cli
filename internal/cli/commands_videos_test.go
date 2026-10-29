package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videoauth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

func TestVideoCLIResultWithoutMediaOrCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HHC_CLI_OPERATIONS_DIR", dir)
	sentinel := filepath.Join(dir, "original-recording")
	os.WriteFile(sentinel, []byte("preserve"), 0600)
	observed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	query := func(_ context.Context, tool string, in videos.Input) (videos.Result, error) {
		if tool != "search" || in.Query != "聚會" || in.Source != "hhc" {
			t.Errorf("%s %+v", tool, in)
		}
		return videos.Result{Sources: []videos.SourceResult{{Source: "hhc", Page: videos.Page{Complete: true, Items: []videos.Video{{ID: "hhc:fixture", Title: "Fixture", ObservedAt: observed}}}}}, ObservedAt: observed}, nil
	}
	var out, diag bytes.Buffer
	c := &commandContext{output: &out, diagnostics: &diag, requestedJSON: true, result: result{SchemaVersion: 1}, videoQuery: query}
	if exit := runVideos(context.Background(), []string{"search", "--query", "聚會", "--source", "hhc", "--json"}, c); exit != 0 {
		t.Fatal(exit, diag.String(), out.String())
	}
	var cliResult struct {
		Data videos.Result `json:"data"`
	}
	if json.Unmarshal(out.Bytes(), &cliResult) != nil {
		t.Fatal(out.String())
	}
	if len(cliResult.Data.Sources) != 1 || cliResult.Data.Sources[0].Items[0].Title != "Fixture" {
		t.Fatal("CLI result changed")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatal("recording touched", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("query initialized recording work")
	}
}
func TestVideoHelpAndFlagIsolation(t *testing.T) {
	for _, args := range [][]string{{"videos", "-h"}, {"videos", "search", "-h"}, {"videos", "search", "--profile", "SECRET", "--json"}, {"videos", "compare", "bad-id", "--json"}, {"videos", "get", "https://bad", "--json"}, {"videos", "search", "--timeout", "31s", "--json"}} {
		var out, diag bytes.Buffer
		exit := Run(context.Background(), args, nil, &out, &diag, "fixture")
		if strings.Contains(out.String()+diag.String(), "SECRET") {
			t.Fatal("parser echoed input")
		}
		help := args[len(args)-1] == "-h"
		if help && exit != 0 || !help && exit != 2 {
			t.Fatalf("%v exit=%d %s", args, exit, out.String())
		}
	}
	if helpRequested([]string{"--query", "-h"}) {
		t.Fatal("keyword interpreted as help")
	}
}

func privateQueryPipe(t *testing.T, expires time.Time) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"youtube": map[string]any{"accessToken": "fixture-private-token", "scope": youtube.ReadScope, "channelId": "UCabcdefghijklmnopqrstuv", "expiresAt": expires}})
	go func() { defer w.Close(); _, _ = w.Write(payload) }()
	t.Cleanup(func() { r.Close() })
	return r
}
func TestPrivateVideoCLIInputAndSafeErrors(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			expires := time.Now().Add(time.Hour)
			if expired {
				expires = time.Now().Add(-time.Hour)
			}
			var out, diag bytes.Buffer
			called := false
			c := &commandContext{input: privateQueryPipe(t, expires), output: &out, diagnostics: &diag, result: result{SchemaVersion: 1}, videoQuery: func(context.Context, string, videos.Input) (videos.Result, error) {
				called = true
				return videos.Result{}, nil
			}}
			exit := runVideos(context.Background(), []string{"get", "youtube:abcdefghijk", "--credentials-stdin", "--json", "--no-input"}, c)
			want := 0
			if expired {
				want = 3
			}
			if exit != want || called == expired {
				t.Fatalf("exit=%d called=%v %s", exit, called, out.String())
			}
			if strings.Contains(out.String()+diag.String(), "fixture-private-token") {
				t.Fatal("credential leaked")
			}
		})
	}
	f, err := os.CreateTemp(t.TempDir(), "credential-file")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if exit := Run(context.Background(), []string{"videos", "get", "youtube:abcdefghijk", "--credentials-stdin", "--json", "--no-input"}, f, &out, io.Discard, "fixture"); exit != 2 {
		t.Fatalf("file accepted: %d %s", exit, out.String())
	}
	out.Reset()
	if exit := Run(context.Background(), []string{"mcp", "serve", "--stdio", "--json"}, nil, &out, io.Discard, "fixture"); exit != 2 {
		t.Fatalf("removed command retained: %d", exit)
	}
}

type videoFixtureTransport func(*http.Request) (*http.Response, error)

func (f videoFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPrivateVideoCompositionOverridesPublicWithoutFallback(t *testing.T) {
	t.Setenv("HHC_YOUTUBE_CHANNEL_ID", "UC1234567890123456789012")
	t.Setenv("HHC_YOUTUBE_API_KEY", "fixture-public-key")
	credentials := videoauth.Credentials{YouTube: &videoauth.YouTubeCredential{AccessToken: "fixture-private-token", Scope: youtube.ReadScope, ChannelID: "UCabcdefghijklmnopqrstuv", ExpiresAt: time.Now().Add(time.Hour)}}
	service := videoService(credentials)
	provider := service.YouTube.(*youtube.Provider)
	calls := 0
	provider.Client.HTTP.Transport = videoFixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "www.googleapis.com" || r.Header.Get("Authorization") != "Bearer fixture-private-token" || r.Header.Get("X-Goog-Api-Key") != "" {
			t.Error("private grant routing")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"items":[{"id":"abcdefghijk","snippet":{"title":"Private fixture","channelId":"UCabcdefghijklmnopqrstuv"},"status":{"privacyStatus":"private"}}]}`))}, nil
	})
	result, err := service.Execute(context.Background(), "get", videos.Input{ID: "youtube:abcdefghijk"})
	if err != nil || result.Incomplete || len(result.Sources[0].Items) != 1 || result.Sources[0].Items[0].Privacy != "private" {
		t.Fatalf("%+v %v", result, err)
	}
	provider.Client.HTTP.Transport = videoFixtureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("do not echo private denial"))}, nil
	})
	result, err = service.Execute(context.Background(), "get", videos.Input{ID: "youtube:abcdefghijk"})
	if err != nil || !result.Incomplete || len(result.Sources[0].Items) != 0 || result.Sources[0].Error.Code != "authentication_required" || calls != 2 {
		t.Fatalf("denial caused fallback: %+v %v calls=%d", result, err, calls)
	}
}

func TestMetadataCLIExplicitApplyAndScopeBoundaries(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			descriptor := map[string]any{"youtube": map[string]any{"accessToken": "fixture-edit-token", "scope": youtube.EditScope, "channelId": "UCabcdefghijklmnopqrstuv", "expiresAt": time.Now().Add(time.Hour)}}
			raw, _ := json.Marshal(descriptor)
			go func() { defer w.Close(); w.Write(raw) }()
			var out bytes.Buffer
			called := false
			c := &commandContext{input: r, output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}, videoEdit: func(_ context.Context, id string, patch youtube.Patch, actualApply bool, etag, hash string, credentials videoauth.Credentials) (youtube.EditResult, error) {
				called = true
				if id != "abcdefghijk" || patch.Title == nil || *patch.Title != "新標題" || patch.Description != nil || patch.Tags != nil || actualApply != apply || credentials.YouTube.Scope != youtube.EditScope {
					t.Error("metadata intent changed")
				}
				if apply && (etag != "fixture-etag" || hash != strings.Repeat("a", 64)) {
					t.Error("preview preconditions lost")
				}
				return youtube.EditResult{DryRun: !apply, Outcome: "planned"}, nil
			}}
			args := []string{"edit", "youtube:abcdefghijk", "--title", "新標題", "--credentials-stdin", "--json", "--no-input"}
			if apply {
				args = append(args, "--apply", "--if-match", "fixture-etag", "--plan-hash", strings.Repeat("a", 64))
			}
			if exit := runVideos(context.Background(), args, c); exit != 0 || !called {
				t.Fatal(exit, out.String())
			}
			if strings.Contains(out.String(), "fixture-edit-token") {
				t.Fatal("edit leaked credential")
			}
		})
	}
	for _, args := range [][]string{
		{"edit", "hhc:abc", "--title", "new"},
		{"edit", "youtube:abcdefghijk", "--privacy", "public"},
		{"edit", "youtube:abcdefghijk", "--title", "new", "--apply"},
		{"edit", "youtube:abcdefghijk", "--tags-json", "null"},
		{"edit", "youtube:abcdefghijk", "--title", "new", "--apply", "--dry-run"},
	} {
		var out bytes.Buffer
		c := &commandContext{output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}}
		if exit := runVideos(context.Background(), append(args, "--json"), c); exit != 2 {
			t.Fatalf("unsafe edit accepted: %v %d", args, exit)
		}
	}
	var out bytes.Buffer
	c := &commandContext{input: privateQueryPipe(t, time.Now().Add(time.Hour)), output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}}
	if exit := runVideos(context.Background(), []string{"edit", "youtube:abcdefghijk", "--title", "new", "--credentials-stdin", "--json"}, c); exit != 4 {
		t.Fatalf("readonly edit scope accepted: %d %s", exit, out.String())
	}
}
