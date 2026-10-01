package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func TestManagedUploadCleansOnlyAfterAuthorityReadyAndResumesWithoutLocalMedia(t *testing.T) {
	for _, mode := range []string{"ready", "validating", "publish-denied", "cleanup-failed"} {
		t.Run(mode, func(t *testing.T) {
			intent := journalIntent(t)
			intent.Publish = mode == "publish-denied"
			if err := os.WriteFile(intent.Input, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			directory, err := j.generatedWorkspace()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cleanup-failed" {
				if err := os.Remove(directory); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(directory, []byte("do not remove"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(directory, "bytes"), []byte("generated"), 0600); err != nil {
				t.Fatal(err)
			}
			state := j.State()
			state.RecordingID, state.PackageID, state.SessionID = "00000000-0000-4000-8000-000000000041", strings.Repeat("b", 32), strings.Repeat("b", 32)
			state.PackageDigest, state.PackageBytes = strings.Repeat("a", 64), 9
			state.SourceFingerprint = media.SourceFingerprint{SHA256: strings.Repeat("c", 64), SizeBytes: 8, ModifiedAt: time.Now().UTC()}
			if err := j.Save(state); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			expiry := now.Add(30 * 24 * time.Hour)
			scopes := []string{"cms:recordings:read", "cms:recordings:write", "cms:recordings:publish"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/account/v1/oauth/token":
					w.Header().Set("Cache-Control", "no-store")
					fmt.Fprintf(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":600,"scope":%q,"principal":{"type":"service","id":%q,"client_id":"client","credential_id":"00000000-0000-4000-8000-000000000012","credential_expires_at":%q}}`, strings.Join(scopes, " "), intent.PrincipalID, now.Add(time.Hour).Format(time.RFC3339))
				case "/api/admin/recordings/" + state.RecordingID + "/packages/" + state.PackageID:
					if r.Method != "GET" {
						t.Error("unexpected package mutation")
					}
					status := "ready"
					if mode == "validating" {
						status = "validating"
					}
					json.NewEncoder(w).Encode(map[string]any{"data": api.Package{RecordingID: state.RecordingID, PackageID: state.PackageID, SessionID: state.SessionID, State: status, ExpiresAt: now.Add(time.Hour), ReadyAt: &now, MediaExpiresAt: &expiry}})
				case "/api/admin/recordings/" + state.RecordingID:
					json.NewEncoder(w).Encode(map[string]any{"data": api.Recording{ID: state.RecordingID, Status: "draft", Version: 4, PackageID: state.PackageID, ReadyAt: &now, ExpiresAt: &expiry}})
				case "/api/admin/recordings/" + state.RecordingID + "/publish":
					if _, err := os.Stat(directory); !os.IsNotExist(err) {
						t.Error("publication ran before generated cleanup")
					}
					w.WriteHeader(http.StatusForbidden)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			origin, _ := url.Parse(server.URL)
			previous := http.DefaultTransport
			http.DefaultTransport = fixtureTransport{origin, server.Client().Transport}
			defer func() { http.DefaultTransport = previous }()
			token, err := auth.NewServiceClient().Exchange(context.Background(), "client", "fixture", scopes)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			client := api.NewClient(token, nil)
			value, err := UploadPrepared(ctx, client, NewUploader(), j)
			switch mode {
			case "ready":
				if err != nil || !value.RequestedActionSatisfied || value.LocalCleanupState != "complete" {
					t.Fatalf("ready %+v %v", value, err)
				}
				// No generated files or FFmpeg bundle are needed for ready recovery.
				if _, err := UploadPrepared(ctx, client, NewUploader(), j); err != nil {
					t.Fatal(err)
				}
			case "validating":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("validation wait: %v", err)
				}
				if _, err := os.Stat(directory); err != nil {
					t.Fatal("cleaned before ready")
				}
			case "publish-denied":
				if err == nil || value.RequestedActionSatisfied || value.LocalCleanupState != "complete" {
					t.Fatalf("publication failure %+v %v", value, err)
				}
			case "cleanup-failed":
				if !errors.Is(err, media.ErrLocalCleanup) || value.RequestedActionSatisfied || value.LocalCleanupState != "failed" {
					t.Fatalf("cleanup failure %+v %v", value, err)
				}
			}
			if mode == "ready" || mode == "publish-denied" {
				if j.State().LastResult == nil || j.State().LastResult.LocalCleanupState != "complete" || j.State().LastResult.CleanupBytesRemaining == nil || *j.State().LastResult.CleanupBytesRemaining != 0 {
					t.Fatal("missing durable cleanup receipt")
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatal("retained generated bytes")
				}
			}
			if b, err := os.ReadFile(intent.Input); err != nil || string(b) != "original" {
				t.Fatal("changed source")
			}
		})
	}
}
