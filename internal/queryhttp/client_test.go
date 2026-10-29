package queryhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

func TestStatusRetryBoundsAndRedaction(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 503, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Method != "GET" {
					t.Error(r.Method)
				}
				if r.Header.Get("Cache-Control") != "no-cache" {
					t.Error("missing revalidation")
				}
				w.Header().Set("Retry-After", "0")
				w.Header().Set("Location", "https://example.invalid/secret")
				w.WriteHeader(status)
				fmt.Fprint(w, "secret-token signed-url")
			}))
			defer s.Close()
			c := New(s.URL)
			permits := 0
			c.Permit = func(context.Context, string, int) error { permits++; return nil }
			err := c.Get(context.Background(), "/fixture", url.Values{}, nil, "test", 1, new(any))
			f := videos.SafeFailure(err)
			want := int32(1)
			if status == 429 || status == 503 {
				want = 2
			}
			if count.Load() != want || permits != int(want) || strings.Contains(err.Error(), "secret") || f.HTTPStatus != status {
				t.Fatalf("count=%d permits=%d failure=%+v", count.Load(), permits, f)
			}
		})
	}
}
func TestLongRetryAfterAndStaleResponses(t *testing.T) {
	for _, header := range []string{"retry", "age", "warning", "badjson", "oversized"} {
		t.Run(header, func(t *testing.T) {
			n := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				w.Header().Set("Content-Type", "application/json")
				switch header {
				case "retry":
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
				case "age":
					w.Header().Set("Age", "31")
				case "warning":
					w.Header().Set("Warning", `110 - "Response is stale"`)
				case "badjson":
					fmt.Fprint(w, "{}")
					return
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", (2<<20)+1))
					return
				}
				fmt.Fprint(w, `{"ok":true}`)
			}))
			defer s.Close()
			if err := New(s.URL).Get(context.Background(), "/", nil, nil, "test", 1, new(any)); err == nil || n != 1 {
				t.Fatalf("err=%v count=%d", err, n)
			}
		})
	}
}
func TestCancelAndPermitDenial(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := New(s.URL).Get(ctx, "/", nil, nil, "test", 1, new(json.RawMessage))
	if err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	c := New(s.URL)
	c.Permit = func(context.Context, string, int) error { return videos.FailureCode("rate_limited") }
	if f := videos.SafeFailure(c.Get(context.Background(), "/", nil, nil, "test", 100, new(any))); f.Code != "rate_limited" {
		t.Fatal(f)
	}
}
