package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServiceExchangeUsesBasicFormAndNeverSerializesBearer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "uploader" || secret != "test-only-secret" || r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
			t.Error("invalid client-secret-basic request")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if len(r.PostForm) != 2 || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("scope") != "cms:recordings:read cms:recordings:write" {
			t.Errorf("invalid form: %v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, `{"access_token":"test-bearer","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:write cms:recordings:read"}`)
	}))
	defer server.Close()
	client := NewServiceClient()
	client.http.Transport = server.Client().Transport
	client.endpoint = server.URL
	before := time.Now()
	token, err := client.Exchange(context.Background(), "uploader", "test-only-secret", []string{"cms:recordings:write", "cms:recordings:read"})
	if err != nil || token.Bearer() != "test-bearer" || token.ExpiresAt().Before(before.Add(580*time.Second)) || token.ExpiresAt().After(time.Now().Add(600*time.Second)) {
		t.Fatalf("invalid token metadata: %v %v", token, err)
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(encoded), fmt.Sprintf("%v", token), fmt.Sprintf("%#v", token)} {
		if strings.Contains(output, "test-bearer") {
			t.Fatal("bearer leaked through output")
		}
	}
}

func TestServiceExchangeRejectsResponseEscalationAndRefresh(t *testing.T) {
	for _, body := range []string{
		`{"access_token":"token","token_type":"Bearer","expires_in":601,"scope":"cms:recordings:read"}`,
		`{"access_token":"token","token_type":"Bearer","expires_in":600,"scope":"*"}`,
		`{"access_token":"token","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read","refresh_token":"not-a-service-token"}`,
		`{"access_token":"token","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read"} {}`,
		`null`, strings.Repeat("x", 65537),
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, body)
		}))
		client := NewServiceClient()
		client.http.Transport = server.Client().Transport
		client.endpoint = server.URL
		_, err := client.Exchange(context.Background(), "uploader", "test-only-secret", []string{"cms:recordings:read"})
		server.Close()
		if !errors.Is(err, ErrInvalidAuthResponse) {
			t.Fatalf("accepted invalid token response (bytes=%d): %v", len(body), err)
		}
	}
}

func TestServiceExchangeDoesNotRedirectCredentialsOrRetryDeniedAuthentication(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusBadRequest, http.StatusServiceUnavailable} {
		requests := 0
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Location", "/steal-secret")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":"invalid_scope","error_description":"test-only-secret"}`)
		}))
		client := NewServiceClient()
		client.http.Transport = server.Client().Transport
		client.endpoint = server.URL
		_, err := client.Exchange(context.Background(), "uploader", "test-only-secret", []string{"cms:recordings:read"})
		server.Close()
		if err == nil || requests != 1 || strings.Contains(err.Error(), "test-only-secret") {
			t.Fatalf("authentication fallback/redirect/leak: %v count=%d", err, requests)
		}
		if status == http.StatusUnauthorized && !errors.Is(err, ErrAuthenticationRequired) {
			t.Fatalf("wrong client rejection: %v", err)
		}
		if status == http.StatusBadRequest && !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("wrong scope rejection: %v", err)
		}
	}
}

func TestServiceExchangeRejectsInvalidScopesAndCancellationBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	client := NewServiceClient()
	client.http.Transport = server.Client().Transport
	client.endpoint = server.URL
	for _, scopes := range [][]string{nil, {"*"}, {"iam:service-principals:write"}, {"cms:recordings:read", "cms:recordings:read"}} {
		if _, err := client.Exchange(context.Background(), "uploader", "test-only-secret", scopes); !errors.Is(err, ErrInvalidAuthInput) {
			t.Fatalf("scope not rejected: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Exchange(ctx, "uploader", "test-only-secret", []string{"cms:recordings:read"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not respected: %v", err)
	}
	if requests != 0 {
		t.Fatalf("invalid/cancelled input sent credentials: %d", requests)
	}
}
