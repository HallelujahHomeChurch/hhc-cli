package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
)

const recordingID = "00000000-0000-4000-8000-000000000041"

type reroute struct {
	transport http.RoundTripper
	origin    *url.URL
}

func (f reroute) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = f.origin.Scheme, f.origin.Host
	r.Host = f.origin.Host
	return f.transport.RoundTrip(r)
}

func apiFixture(t *testing.T, handler http.HandlerFunc, changedIdentity bool, scopes ...string) (*Client, *int) {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{"cms:recordings:read"}
	}
	grants := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/v1/oauth/token" {
			handler(w, r)
			return
		}
		grants++
		id := "00000000-0000-4000-8000-000000000011"
		if changedIdentity && grants > 1 {
			id = "00000000-0000-4000-8000-000000000099"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":600,"scope":%q,"principal":{"type":"service","id":%q,"client_id":"fixture","credential_id":"00000000-0000-4000-8000-000000000012","credential_expires_at":%q}}`, grants, strings.Join(scopes, " "), id, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	t.Cleanup(server.Close)
	origin, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = reroute{server.Client().Transport, origin}
	t.Cleanup(func() { http.DefaultTransport = previous })
	renew := func(ctx context.Context) (auth.Token, error) {
		return auth.NewServiceClient().Exchange(ctx, "fixture", "fixture-secret", scopes)
	}
	token, err := renew(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(token, renew), &grants
}

func TestGetRenewsOnceWithoutChangingIdentity(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			calls := 0
			client, grants := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/admin/recordings/"+recordingID || r.Method != "GET" || r.Header.Get("Cookie") != "" {
					t.Error("wrong request")
				}
				if calls == 1 {
					w.WriteHeader(401)
					return
				}
				if r.Header.Get("Authorization") != "Bearer token-2" {
					t.Error("did not use renewed token")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"data":{"id":%q,"title":"主日","status":"draft","version":1},"meta":{},"error":null}`, recordingID)
			}, changed)
			value, err := client.GetRecording(context.Background(), recordingID)
			if changed {
				if !errors.Is(err, auth.ErrAuthenticationRequired) || calls != 1 {
					t.Fatalf("identity fallback: %v calls=%d", err, calls)
				}
			} else if err != nil || value.ID != recordingID || calls != 2 {
				t.Fatalf("renewal: %v calls=%d", err, calls)
			}
			if *grants != 2 {
				t.Fatalf("unbounded renewal: %d", *grants)
			}
		})
	}
}

func TestGetRejectsErrorsRedirectsMalformedAndMismatchedRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"forbidden", 403, "secret diagnostic", "permission_denied"},
		{"redirect", 307, "secret diagnostic", "invalid_api_response"},
		{"busy", 429, "secret diagnostic", "rate_limited"},
		{"unavailable", 503, "secret diagnostic", "api_unavailable"},
		{"null", 200, `{"data":null}`, "invalid_api_response"},
		{"wrong record", 200, `{"data":{"id":"00000000-0000-4000-8000-000000000099","title":"other","version":1,"status":"draft"}}`, "invalid_api_response"},
		{"trailing", 200, `{"data":{}} {}`, "invalid_api_response"},
		{"wrong success status", 202, `{"data":{"id":"00000000-0000-4000-8000-000000000041","title":"other","version":1,"status":"draft"}}`, "invalid_api_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, grants := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "https://attacker.invalid")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}, false)
			_, err := client.GetRecording(context.Background(), recordingID)
			if err == nil || err.Error() != tc.code || calls != 1 || *grants != 1 {
				t.Fatalf("incorrect bounded error: %v calls=%d grants=%d", err, calls, *grants)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("remote body leaked")
			}
		})
	}
}

func TestGetBoundsUnauthorizedRetryAndRejectsInvalidID(t *testing.T) {
	calls := 0
	client, grants := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}, false)
	for _, id := range []string{"../token", "00000000-0000-0000-0000-000000000000", ""} {
		if _, err := client.GetRecording(context.Background(), id); !errors.Is(err, auth.ErrInvalidAuthInput) {
			t.Fatalf("invalid ID accepted: %v", err)
		}
	}
	if calls != 0 || *grants != 1 {
		t.Fatal("invalid input reached network")
	}
	if _, err := client.GetRecording(context.Background(), recordingID); !errors.Is(err, auth.ErrAuthenticationRequired) || calls != 2 || *grants != 2 {
		t.Fatalf("unbounded unauthorized retry: %v calls=%d grants=%d", err, calls, *grants)
	}
}
