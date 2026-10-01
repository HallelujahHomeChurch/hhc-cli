package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

func TestPackageControlRejectsUnboundOrIncompleteReady(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	for _, state := range []string{"ready", "invented"} {
		t.Run(state, func(t *testing.T) {
			client, _ := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"data":{"packageId":%q,"sessionId":%q,"recordingId":%q,"state":%q,"expiresAt":%q}}`, pkg, pkg, recordingID, state, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
			}, false)
			if _, err := client.PackageStatus(context.Background(), recordingID, pkg, ""); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("accepted invalid state: %v", err)
			}
		})
	}
}

func TestSignPackageBindsRequestedObjectsAndRedactsCapabilities(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	for _, path := range []string{"master.m3u8", "720p/index.m3u8"} {
		t.Run(path, func(t *testing.T) {
			client, _ := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != packagePath(recordingID, pkg)+"/sign" {
					t.Error("wrong sign route")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"data":[{"path":%q,"url":"https://fixture.invalid/private-secret","method":"PUT","headers":{"Content-Type":["application/octet-stream"]}}]}`, path)
			}, false, "cms:recordings:write")
			value, err := client.SignPackage(context.Background(), recordingID, pkg, []string{"master.m3u8"})
			if path != "master.m3u8" {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("accepted other object: %v", err)
				}
				return
			}
			if err != nil || len(value) != 1 {
				t.Fatalf("sign failed: %v", err)
			}
			encoded, _ := json.Marshal(value)
			if strings.Contains(string(encoded), "private-secret") || strings.Contains(fmt.Sprintf("%+v", value), "private-secret") {
				t.Fatal("capability leaked")
			}
		})
	}
}

func TestPublishKeepsOriginalPreconditionAndReportsManualUnpublish(t *testing.T) {
	client, _ := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/admin/recordings/"+recordingID+"/publish" || r.Header.Get("If-Match") != `"4"` || r.Header.Get("Idempotency-Key") != "operation:publish" {
			t.Error("lost publish intent")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"receipt":{"operationKey":"operation:publish","actorType":"service","actorId":"00000000-0000-4000-8000-000000000011","recordingId":%q,"packageId":"0123456789abcdef0123456789abcdef","expectedVersion":4,"publishedVersion":5,"publishedAt":"2026-10-01T00:00:00Z"},"current":{"id":%q,"status":"draft","version":6},"outcome":"state_changed"}}`, recordingID, recordingID)
	}, false, "cms:recordings:publish")
	value, err := client.PublishRecording(context.Background(), recordingID, 4, "operation:publish")
	if err != nil || value.Outcome != "state_changed" {
		t.Fatalf("replayed publication: %+v %v", value, err)
	}
}

func packageInventory() media.RecordingPackageInventory {
	inv := media.RecordingPackageInventory{SchemaVersion: 1, PresetVersion: "hls-v1", InventoryDigest: "b29d5b3efa97e926f585b0c62a480d45465730f16edd8059d4fb16792629e58b", Renditions: []media.RecordingRendition{{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 5, SegmentCount: 1}}}
	for n, path := range []string{"master.m3u8", "720p/index.m3u8", "720p/init.mp4", "720p/seg-000000.m4s"} {
		inv.Objects = append(inv.Objects, media.RecordingPackageObject{Path: path, SizeBytes: 100, SHA256: strings.Repeat(string(rune('a'+n)), 64)})
	}
	return inv
}

func TestPackageControlUsesDurableKeysAndDoesNotTreatAcceptedAsReady(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	calls := 0
	client, _ := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer token-1" || r.Header.Get("Cookie") != "" {
			t.Error("wrong credential boundary")
		}
		switch calls {
		case 1:
			if r.URL.Path != "/api/admin/recordings" || r.Method != "POST" || r.Header.Get("Idempotency-Key") != "operation:create" {
				t.Error("wrong creation contract")
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"data":{"id":%q,"title":"Gathering","status":"draft","version":1}}`, recordingID)
		case 2:
			var inv media.RecordingPackageInventory
			if json.NewDecoder(r.Body).Decode(&inv) != nil || inv.InventoryDigest != packageInventory().InventoryDigest || r.Header.Get("Idempotency-Key") != "operation:package" {
				t.Error("wrong package intent")
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"data":{"packageId":%q,"sessionId":%q,"recordingId":%q,"state":"uploading","expiresAt":%q,"inventory":{"inventoryDigest":%q}}}`, pkg, pkg, recordingID, expiry, inv.InventoryDigest)
		case 3:
			if r.Method != "GET" || r.URL.Query().Get("limit") != "1000" || r.URL.Query().Get("cursor") != "720p/init.mp4" {
				t.Error("wrong paging")
			}
			fmt.Fprintf(w, `{"data":{"packageId":%q,"sessionId":%q,"recordingId":%q,"state":"uploading","expiresAt":%q,"confirmedObjects":["master.m3u8"],"nextCursor":""}}`, pkg, pkg, recordingID, expiry)
		case 4:
			if !strings.HasSuffix(r.URL.Path, "/complete") || r.Method != "POST" {
				t.Error("wrong completion")
			}
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"data":{"packageId":%q,"sessionId":%q,"recordingId":%q,"state":"freezing","expiresAt":%q}}`, pkg, pkg, recordingID, expiry)
		default:
			t.Error("unexpected retry")
		}
	}, false, "cms:recordings:read", "cms:recordings:write")
	ctx := context.Background()
	if _, err := client.CreateRecording(ctx, "Gathering", "operation:create"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreatePackage(ctx, recordingID, packageInventory(), "operation:package"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PackageStatus(ctx, recordingID, pkg, "720p/init.mp4"); err != nil {
		t.Fatal(err)
	}
	value, err := client.CompletePackage(ctx, recordingID, pkg)
	if err != nil || value.State != "freezing" || calls != 4 {
		t.Fatalf("completion: %+v %v", value, err)
	}
}
