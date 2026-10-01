package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

type memoryCredentials struct {
	value         []byte
	failSave      bool
	failAt, saves int
}

func (s *memoryCredentials) Load(string) ([]byte, error) {
	if len(s.value) == 0 {
		return nil, ErrCredentialNotFound
	}
	return append([]byte(nil), s.value...), nil
}
func (s *memoryCredentials) Save(_ string, value []byte) error {
	s.saves++
	if s.failSave || s.saves == s.failAt {
		return ErrCredentialStoreUnavailable
	}
	s.value = append([]byte(nil), value...)
	return nil
}

func TestProfileFailedRotationSaveReturnsNoToken(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(withPrincipal(`{"access_token":"fresh","refresh_token":"rotated","token_type":"Bearer","expires_in":900,"scope":"cms:recordings:read offline_access"}`, "human", "hhc-cli")))
	}))
	defer s.Close()
	p := NewProfiles(t.TempDir())
	store := &memoryCredentials{failAt: 2}
	p.store = store
	store.value, _ = json.Marshal(savedProfile{Version: 1, Kind: "human", DeviceID: strings.Repeat("d", 43), Scope: "cms:recordings:read offline_access", Secret: "old", Principal: profilePrincipal("human", "hhc-cli")})
	p.human.http.Transport = s.Client().Transport
	p.human.tokenEndpoint = s.URL
	got, err := p.Token(context.Background(), "personal")
	if !errors.Is(err, ErrCredentialStoreUnavailable) || got.Bearer() != "" {
		t.Fatalf("used non-durable rotation: %v", err)
	}
	var saved savedProfile
	json.Unmarshal(store.value, &saved)
	if !saved.Rotating || saved.Secret != "old" {
		t.Fatal("lost rotation fence")
	}
}

func TestProfileLogoutClearsLocalAfterRemoteFailure(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer s.Close()
	p := NewProfiles(t.TempDir())
	store := &memoryCredentials{}
	p.store = store
	store.value, _ = json.Marshal(savedProfile{Version: 1, Kind: "human", DeviceID: strings.Repeat("d", 43), Scope: "offline_access", Secret: "old", Rotating: true, Principal: profilePrincipal("human", "hhc-cli")})
	p.human.http.Transport = s.Client().Transport
	p.human.revokeEndpoint = s.URL
	result, err := p.Logout(context.Background(), "personal")
	if err == nil || !result.LocalCleared || result.RemoteRevoked || len(store.value) != 0 {
		t.Fatalf("logout result: %+v %v", result, err)
	}
}

func TestProfileServiceLoginPersistsOnlyAfterSuccessfulExchange(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(status)
				w.Write([]byte(withPrincipal(`{"access_token":"service-token","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read"}`, "service", "client")))
			}))
			defer s.Close()
			p := NewProfiles(t.TempDir())
			store := &memoryCredentials{}
			p.store = store
			p.service.http.Transport = s.Client().Transport
			p.service.endpoint = s.URL
			_, err := p.LoginService(context.Background(), "uploader", "client", "secret", []string{"cms:recordings:read"})
			if status == 200 {
				var saved savedProfile
				json.Unmarshal(store.value, &saved)
				if err != nil || saved.Kind != "service" || saved.Secret != "secret" || saved.Principal.ID != "00000000-0000-4000-8000-000000000011" {
					t.Fatalf("service login: %v", err)
				}
			} else if !errors.Is(err, ErrAuthenticationRequired) || len(store.value) != 0 {
				t.Fatalf("saved rejected credential: %v", err)
			}
		})
	}
}

func TestProfileNoInputHumanLoginDoesNotTouchStore(t *testing.T) {
	p := NewProfiles(t.TempDir())
	p.store = nil
	_, err := p.LoginHuman(context.Background(), "personal", HumanLoginOptions{NoInput: true})
	if !errors.Is(err, ErrAuthenticationRequired) {
		t.Fatalf("no-input login: %v", err)
	}
}
func (s *memoryCredentials) Delete(string) error { s.value = nil; return nil }

func TestProfileRefreshPersistsBeforeReturningAndFencesAmbiguousRotation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "persist", true: "lost response"}[fail], func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if fail {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Write([]byte(withPrincipal(`{"access_token":"fresh","refresh_token":"rotated","token_type":"Bearer","expires_in":900,"scope":"offline_access"}`, "human", "hhc-cli")))
			}))
			defer s.Close()
			p := NewProfiles(t.TempDir())
			store := &memoryCredentials{}
			p.store = store
			p.human.http.Transport = s.Client().Transport
			p.human.tokenEndpoint = s.URL
			initial := savedProfile{Version: 1, Kind: "human", DeviceID: strings.Repeat("d", 43), Scope: "cms:recordings:read offline_access", Secret: "old", Principal: profilePrincipal("human", "hhc-cli")}
			store.value, _ = json.Marshal(initial)
			token, err := p.Token(context.Background(), "personal")
			if fail {
				if !errors.Is(err, ErrAuthUnavailable) {
					t.Fatalf("refresh: %v", err)
				}
				_, err = p.Token(context.Background(), "personal")
				if !errors.Is(err, ErrAuthenticationRequired) || calls.Load() != 1 {
					t.Fatalf("reused ambiguous credential: %v calls=%d", err, calls.Load())
				}
			} else {
				if err != nil || token.Scope() != "offline_access" {
					t.Fatalf("rotation: %v", err)
				}
				var stored savedProfile
				json.Unmarshal(store.value, &stored)
				if stored.Secret != "rotated" || stored.Rotating || stored.Scope != "offline_access" {
					t.Fatal("rotation not durable before return")
				}
			}
			filepath.WalkDir(p.directory, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					data, _ := os.ReadFile(path)
					if strings.Contains(string(data), "old") || strings.Contains(string(data), "rotated") {
						t.Error("secret in profile directory")
					}
				}
				return nil
			})
		})
	}
}

func TestProfileLockAndStoreFailurePreventRefresh(t *testing.T) {
	p := NewProfiles(t.TempDir())
	s := &memoryCredentials{failSave: true}
	p.store = s
	s.value, _ = json.Marshal(savedProfile{Version: 1, Kind: "human", DeviceID: strings.Repeat("d", 43), Scope: "cms:recordings:read offline_access", Secret: "old", Principal: profilePrincipal("human", "hhc-cli")})
	_, err := p.Token(context.Background(), "personal")
	if !errors.Is(err, ErrCredentialStoreUnavailable) {
		t.Fatalf("did not fence before network: %v", err)
	}
	lock, err := operation.LockWorkspace(filepath.Join(p.directory, "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = p.Token(context.Background(), "personal")
	if !errors.Is(err, operation.ErrOperationBusy) {
		t.Fatalf("concurrent refresh not locked: %v", err)
	}
}
