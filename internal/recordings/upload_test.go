package recordings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestUploadResumesServerConfirmedPackageAndWaitsForReady(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) { testUploadResume(t, missing, 0, false, false) })
	}
	for _, rejections := range []int{1, 2} {
		t.Run(fmt.Sprintf("url-rejections-%d", rejections), func(t *testing.T) { testUploadResume(t, true, rejections, false, false) })
	}
	t.Run("lost-complete-response", func(t *testing.T) { testUploadResume(t, false, 0, true, false) })
	t.Run("publish-ready", func(t *testing.T) { testUploadResume(t, false, 0, false, true) })
}

func testUploadResume(t *testing.T, missing bool, rejections int, lostComplete bool, publish bool) {
	intent := journalIntent(t)
	intent.Prepare = false
	intent.Publish = publish
	intent.Input = t.TempDir()
	paths := []string{"master.m3u8", "720p/index.m3u8", "720p/init.mp4", "720p/seg-000000.m4s"}
	if err := os.Mkdir(filepath.Join(intent.Input, "720p"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.WriteFile(filepath.Join(intent.Input, filepath.FromSlash(path)), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := media.BuildPackageInventory(context.Background(), intent.Input, []media.RecordingRendition{{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 5, SegmentCount: 1}}, "hls-v1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(inv)
	if err := os.WriteFile(filepath.Join(intent.Input, "package.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	state := j.State()
	state.RecordingID = "00000000-0000-4000-8000-000000000041"
	state.PackageID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	state.SessionID = state.PackageID
	state.PackageDigest = inv.InventoryDigest
	if err := j.Save(state); err != nil {
		t.Fatal(err)
	}
	completed := false
	queries := 0
	puts, signs := 0, 0
	publications := 0
	scopes := []string{"cms:recordings:read", "cms:recordings:write"}
	if publish {
		scopes = append(scopes, "cms:recordings:publish")
	}
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/account/v1/oauth/token" {
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprintf(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":600,"scope":%q,"principal":{"type":"service","id":%q,"client_id":"client","credential_id":"00000000-0000-4000-8000-000000000012","credential_expires_at":%q}}`, strings.Join(scopes, " "), intent.PrincipalID, now.Add(time.Hour).Format(time.RFC3339))
			return
		}
		if r.Method == "PUT" {
			puts++
			body, _ := io.ReadAll(r.Body)
			if !missing || string(body) != "master.m3u8" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("unsafe or redundant byte upload")
			}
			if puts <= rejections {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(200)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing auth")
		}
		base := "/api/admin/recordings/" + state.RecordingID + "/packages/" + state.PackageID
		if r.URL.Path == "/api/admin/recordings/"+state.RecordingID {
			fmt.Fprintf(w, `{"data":{"id":%q,"status":"draft","version":4,"packageId":%q,"readyAt":%q,"expiresAt":%q}}`, state.RecordingID, state.PackageID, now.Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339))
			return
		}
		if r.URL.Path == "/api/admin/recordings/"+state.RecordingID+"/publish" {
			publications++
			if !completed || r.Method != "POST" || r.Header.Get("If-Match") != `"4"` || r.Header.Get("Idempotency-Key") != state.OperationID+":publish" {
				t.Error("incorrect publication intent")
			}
			expiry := now.Add(30 * 24 * time.Hour)
			json.NewEncoder(w).Encode(map[string]any{"data": api.PublishResult{Receipt: api.PublishReceipt{OperationKey: state.OperationID + ":publish", ActorType: intent.PrincipalType, ActorID: intent.PrincipalID, RecordingID: state.RecordingID, PackageID: state.PackageID, ExpectedVersion: 4, PublishedVersion: 5, PublishedAt: now}, Current: api.Recording{ID: state.RecordingID, Status: "published", Version: 5, PackageID: state.PackageID, ReadyAt: &now, ExpiresAt: &expiry}, Outcome: "published"}})
			return
		}
		value := api.Package{PackageID: state.PackageID, SessionID: state.PackageID, RecordingID: state.RecordingID, State: "uploading", ExpiresAt: now.Add(time.Hour), ConfirmedObjects: paths}
		switch {
		case r.Method == "GET" && r.URL.Path == base:
			queries++
			if missing {
				value.ConfirmedObjects = paths[1:]
			}
			if completed {
				value.State = "ready"
				value.ReadyAt = &now
				expiry := now.Add(30 * 24 * time.Hour)
				value.MediaExpiresAt = &expiry
			}
		case r.Method == "POST" && r.URL.Path == base+"/complete":
			if completed {
				t.Error("completion replayed without state reconciliation")
			}
			completed = true
			value.State = "freezing"
			if lostComplete {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(202)
		case r.Method == "POST" && r.URL.Path == base+"/sign":
			signs++
			var input struct {
				Paths []string `json:"paths"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Paths) != 1 || input.Paths[0] != "master.m3u8" {
				t.Error("signed already-confirmed objects")
			}
			query := url.Values{"X-Amz-Date": {now.Format("20060102T150405Z")}, "X-Amz-Expires": {"900"}, "X-Amz-SignedHeaders": {"content-length;content-type;host"}, "X-Amz-Signature": {strings.Repeat("a", 64)}}
			target := "https://" + strings.Repeat("a", 32) + ".r2.cloudflarestorage.com/test-bucket/recordings/packages/" + state.PackageID + "/staging/master.m3u8?" + query.Encode()
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"path": "master.m3u8", "url": target, "method": "PUT", "headers": http.Header{"Content-Type": {"application/vnd.apple.mpegurl"}, "Content-Length": {"11"}}}}})
			return
		default:
			t.Errorf("unexpected upload or mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": value})
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := UploadPrepared(ctx, api.NewClient(token, nil), NewUploader(), j)
	if rejections == 2 {
		if err != ErrUploadURLRejected || puts != 2 || signs != 2 || completed {
			t.Fatalf("unbounded re-sign puts=%d signs=%d completed=%v err=%v", puts, signs, completed, err)
		}
		return
	}
	if err != nil || value.Package.State != "ready" || queries != 2+rejections || !completed {
		t.Fatalf("upload result %+v %v queries=%d", value, err, queries)
	}
	if missing && (puts != 1+rejections || signs != 1+rejections) || !missing && (puts != 0 || signs != 0) {
		t.Fatalf("wrong transfer counts: puts=%d signs=%d", puts, signs)
	}
	if !value.RequestedActionSatisfied || publish && (publications != 1 || j.State().PublishExpectedVersion != 4) {
		t.Fatal("publication was not confirmed or persisted")
	}
	if _, err := os.Stat(filepath.Join(intent.Input, "package.json")); err != nil {
		t.Fatal("deleted user package")
	}
}
