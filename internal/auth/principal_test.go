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

func principalFixture(kind, client string) string {
	credential := ""
	if kind == "service" {
		credential = `,"credential_id":"00000000-0000-4000-8000-000000000012"`
	}
	return fmt.Sprintf(`"principal":{"type":%q,"id":"00000000-0000-4000-8000-000000000011","client_id":%q,"credential_expires_at":%q%s}`, kind, client, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), credential)
}

func TestProfileRejectsIdentityChangeOnRenewal(t *testing.T) {
	for _, kind := range []string{"human", "service"} {
		t.Run(kind, func(t *testing.T) {
			client := "hhc-cli"
			body := `{"access_token":"fresh","refresh_token":"rotated","token_type":"Bearer","expires_in":600,"scope":"offline_access"}`
			initial := savedProfile{Version: 1, Kind: kind, DeviceID: strings.Repeat("d", 43), Scope: "offline_access", Secret: "old"}
			if kind == "service" {
				client = "uploader"
				initial.ClientID, initial.DeviceID, initial.Scope = client, "", "cms:recordings:read"
				body = `{"access_token":"fresh","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read"}`
			}
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				fmt.Fprint(w, strings.ReplaceAll(withPrincipal(body, kind, client), "00000000-0000-4000-8000-000000000011", "00000000-0000-4000-8000-000000000099"))
			}))
			defer s.Close()
			p := NewProfiles(t.TempDir())
			initial.Principal = profilePrincipal(kind, client)
			encoded, _ := json.Marshal(initial)
			store := &memoryCredentials{value: encoded}
			p.store = store
			p.human.http.Transport, p.human.tokenEndpoint = s.Client().Transport, s.URL
			p.service.http.Transport, p.service.endpoint = s.Client().Transport, s.URL
			token, err := p.Token(context.Background(), "profile")
			if !errors.Is(err, ErrAuthenticationRequired) || token.Bearer() != "" {
				t.Fatalf("identity switch was not fenced: %v", err)
			}
			if kind == "human" {
				var saved savedProfile
				json.Unmarshal(store.value, &saved)
				if !saved.Rotating || saved.Secret != "old" {
					t.Fatal("identity mismatch lost the refresh fence")
				}
			}
		})
	}
}

func withPrincipal(body, kind, client string) string {
	return strings.TrimSuffix(body, "}") + "," + principalFixture(kind, client) + "}"
}

func profilePrincipal(kind, client string) Principal {
	var response oauthResponse
	json.Unmarshal([]byte("{"+principalFixture(kind, client)+"}"), &response)
	return *response.Principal
}

func TestExchangeBindsIssuerIdentityAndRejectsWrongClient(t *testing.T) {
	for _, clientID := range []string{"uploader", "different"} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, withPrincipal(`{"access_token":"token","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read"}`, "service", clientID))
		}))
		c := NewServiceClient()
		c.http.Transport = s.Client().Transport
		c.endpoint = s.URL
		token, err := c.Exchange(context.Background(), "uploader", "secret", []string{"cms:recordings:read"})
		s.Close()
		if clientID != "uploader" {
			if err == nil {
				t.Fatal("accepted another client's identity")
			}
			continue
		}
		if err != nil || token.Principal().ID != "00000000-0000-4000-8000-000000000011" {
			t.Fatalf("issuer identity missing: %v", err)
		}
		data, _ := json.Marshal(token)
		if string(data) != "{}" {
			t.Fatal("token became JSON visible")
		}
	}
}
