package recordings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverPreflightSnapshotAndSchema(t *testing.T) {
	intent := journalIntent(t)
	intent.CoverPath = filepath.Join(t.TempDir(), "聚會 封面.png")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 160, 90))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intent.CoverPath, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := SnapshotCover(j); err != nil {
		t.Fatal(err)
	}
	if j.State().SchemaVersion != 2 || j.State().Cover == nil || j.State().Cover.SizeBytes != int64(b.Len()) {
		t.Fatal("cover intent lost")
	}
	if err := os.Remove(intent.CoverPath); err != nil {
		t.Fatal(err)
	}
	if _, err := coverBytes(j); err != nil {
		t.Fatal("owned snapshot needs original", err)
	}
}

func TestReadyCoverFlowAndReceiptResume(t *testing.T) {
	for _, mode := range []string{"selected", "failed", "expired", "uploading", "lost-upload", "lost-selection", "manual-change", "unpublished"} {
		t.Run(mode, func(t *testing.T) {
			intent := journalIntent(t)
			intent.CoverPath = filepath.Join(t.TempDir(), "封面 image.png")
			var imageData bytes.Buffer
			if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 160, 90))); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(intent.CoverPath, imageData.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if err := SnapshotCover(j); err != nil {
				t.Fatal(err)
			}
			generated, err := j.generatedWorkspace()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(generated, "owned"), []byte("HLS"), 0600); err != nil {
				t.Fatal(err)
			}
			state := j.State()
			state.RecordingID = "00000000-0000-4000-8000-000000000041"
			state.PackageID = strings.Repeat("b", 32)
			state.SessionID = state.PackageID
			state.PackageDigest = strings.Repeat("a", 64)
			if err := j.Save(state); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			expiry := now.Add(30 * 24 * time.Hour)
			version := int64(4)
			selected := ""
			posts, puts, publishes := 0, 0, 0
			publication := "draft"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/account/v1/oauth/token" {
					w.Header().Set("Cache-Control", "no-store")
					fmt.Fprintf(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":600,"scope":"cms:recordings:read cms:recordings:write cms:recordings:publish","principal":{"type":"service","id":%q,"client_id":"client","credential_id":"00000000-0000-4000-8000-000000000012","credential_expires_at":%q}}`, intent.PrincipalID, expiry.Format(time.RFC3339))
					return
				}
				recording := api.Recording{ID: state.RecordingID, Version: version, Status: publication, PackageID: state.PackageID, ReadyAt: &now, ExpiresAt: &expiry}
				switch {
				case strings.HasSuffix(r.URL.Path, "/packages/"+state.PackageID):
					json.NewEncoder(w).Encode(map[string]any{"data": api.Package{RecordingID: state.RecordingID, PackageID: state.PackageID, SessionID: state.SessionID, State: "ready", ExpiresAt: expiry, ReadyAt: &now, MediaExpiresAt: &expiry}})
				case strings.HasSuffix(r.URL.Path, "/covers"):
					items := []api.CoverItem{}
					if posts > 0 {
						status := "ready"
						if mode == "failed" {
							status = "failed"
						}
						if mode == "expired" && posts == 1 {
							status = "expired"
						}
						if mode == "uploading" && posts == 1 {
							status = "uploading"
						}
						count := posts
						if mode == "uploading" {
							count = 1
						}
						items = append(items, api.CoverItem{ID: "cover-1", UploadID: fmt.Sprintf("upload-%d", count), Kind: "custom", State: status, OperationKey: fmt.Sprintf("%s:cover:%d", state.OperationID, count)})
					}
					json.NewEncoder(w).Encode(map[string]any{"data": api.CoverList{Items: items, SelectedCoverID: selected, RecordingVersion: version}})
				case strings.HasSuffix(r.URL.Path, "/cover-uploads"):
					posts++
					if mode == "expired" && (j.State().Cover.Attempt != posts || r.Header.Get("Idempotency-Key") != j.State().Cover.AttemptKey) {
						t.Error("attempt was not persisted before upload")
					}
					if mode == "lost-upload" && posts == 1 {
						if err := j.root.Remove("cover.snapshot"); err != nil {
							t.Error(err)
						}
						if err := os.Remove(intent.CoverPath); err != nil {
							t.Error(err)
						}
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(202)
					count := posts
					if mode == "uploading" {
						count = 1
					}
					json.NewEncoder(w).Encode(map[string]any{"data": api.CoverUpload{UploadID: fmt.Sprintf("upload-%d", count), State: "pending"}})
				case strings.HasSuffix(r.URL.Path, "/cover"):
					puts++
					if _, err := os.Stat(generated); !os.IsNotExist(err) {
						t.Error("cover before HLS cleanup")
					}
					if r.Header.Get("If-Match") != `"4"` {
						t.Error("selection precondition changed")
					}
					selected = "cover-1"
					version = 5
					recording.Version = version
					if mode == "lost-selection" && puts == 1 {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"data": api.CoverSelection{Recording: recording, CoverID: selected, Outcome: "selected", Receipt: api.CoverReceipt{CoverID: selected, RecordingVersion: 5}}})
				case strings.HasSuffix(r.URL.Path, "/publish"):
					publishes++
					if r.Header.Get("If-Match") != `"5"` {
						t.Error("publish did not bind selection version")
					}
					publication = "published"
					version = 6
					recording.Status = publication
					recording.Version = version
					outcome := "published"
					if mode == "unpublished" && publishes > 1 {
						publication = "draft"
						version = 7
						recording.Status = publication
						recording.Version = version
						outcome = "state_changed"
					}
					json.NewEncoder(w).Encode(map[string]any{"data": api.PublishResult{Receipt: api.PublishReceipt{OperationKey: state.OperationID + ":publish", ActorType: intent.PrincipalType, ActorID: intent.PrincipalID, RecordingID: state.RecordingID, PackageID: state.PackageID, ExpectedVersion: 5, PublishedVersion: 6, PublishedAt: now}, Current: recording, Outcome: outcome}})
				case r.URL.Path == "/api/admin/recordings/"+state.RecordingID:
					json.NewEncoder(w).Encode(map[string]any{"data": recording})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			origin, _ := url.Parse(server.URL)
			previous := http.DefaultTransport
			http.DefaultTransport = fixtureTransport{origin, server.Client().Transport}
			defer func() { http.DefaultTransport = previous }()
			token, err := auth.NewServiceClient().Exchange(context.Background(), "client", "fixture", []string{"cms:recordings:read", "cms:recordings:write", "cms:recordings:publish"})
			if err != nil {
				t.Fatal(err)
			}
			c := api.NewClient(token, nil)
			value, err := UploadPrepared(context.Background(), c, NewUploader(), j)
			if mode == "failed" {
				if err == nil || publishes != 0 || value.RequestedActionSatisfied || value.ValidationState != "ready" {
					t.Fatalf("failed cover published: %+v %v", value, err)
				}
				return
			}
			if err != nil || value.CoverState != "selected" || value.CoverID != "cover-1" || !value.RequestedActionSatisfied {
				t.Fatalf("cover result %+v %v", value, err)
			}
			if err := os.Remove(intent.CoverPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if mode == "manual-change" {
				selected = "cover-2"
				version++
			}
			if mode == "unpublished" {
				publication = "draft"
				version = 7
			}
			value, err = UploadPrepared(context.Background(), c, NewUploader(), j)
			if mode == "manual-change" || mode == "unpublished" {
				if err == nil || value.RequestedActionSatisfied {
					t.Fatal("changed state silently restored")
				}
			} else if err != nil {
				t.Fatal("resume needs original", err)
			}
			expectedPuts := 1
			if mode == "lost-selection" {
				expectedPuts = 2
			}
			expectedPosts := 1
			if mode == "expired" || mode == "uploading" {
				expectedPosts = 2
			}
			if posts != expectedPosts || puts != expectedPuts {
				t.Fatal("cover replayed", posts, puts)
			}
		})
	}
}

func TestMissingSnapshotStopsBeforeLocalOrRemoteHLS(t *testing.T) {
	intent := journalIntent(t)
	intent.CoverPath = filepath.Join(t.TempDir(), "cover.png")
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 160, 90)))
	os.WriteFile(intent.CoverPath, b.Bytes(), 0600)
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := SnapshotCover(j); err != nil {
		t.Fatal(err)
	}
	j.root.Remove("cover.snapshot")
	os.WriteFile(intent.CoverPath, []byte("changed"), 0600)
	if err := ensureCoverSnapshot(context.Background(), nil, j); !errors.Is(err, ErrCoverInput) {
		t.Fatal("resume bypassed pinned image", err)
	}
}

func TestCoverRejectsEXIFOrientation(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 160, 90)), nil); err != nil {
		t.Fatal(err)
	}
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	data := append([]byte{}, b.Bytes()[:2]...)
	data = append(data, 0xff, 0xe1, 0, byte(len(exif)+2))
	data = append(data, exif...)
	data = append(data, b.Bytes()[2:]...)
	if _, err := validateCover(data); !errors.Is(err, ErrCoverOrientation) {
		t.Fatalf("orientation not rejected: %v", err)
	}
}

func TestCoverSweptSnapshotRequiresExactOriginal(t *testing.T) {
	intent := journalIntent(t)
	intent.CoverPath = filepath.Join(t.TempDir(), "cover.png")
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 160, 90)))
	os.WriteFile(intent.CoverPath, b.Bytes(), 0600)
	base := t.TempDir()
	j, err := OpenJournal(base, journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := SnapshotCover(j); err != nil {
		t.Fatal(err)
	}
	j.Close()
	report, err := SweepExpired(base, time.Now().Add(25*time.Hour))
	if err != nil || report.Removed != 1 {
		t.Fatalf("sweep %+v %v", report, err)
	}
	j, err = OpenJournal(base, journalFixtureID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := coverBytes(j); err != nil {
		t.Fatal("same hash not rebuilt", err)
	}
	if err := j.root.Remove("cover.snapshot"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(intent.CoverPath, []byte("changed"), 0600)
	if _, err := coverBytes(j); !errors.Is(err, ErrCoverInput) {
		t.Fatal("changed original accepted", err)
	}
}

func TestJournalRejectsSchemaOneCoverIntent(t *testing.T) {
	intent := journalIntent(t)
	intent.CoverPath = filepath.Join(t.TempDir(), "cover.png")
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	state := j.State()
	state.SchemaVersion = 1
	if err := j.Save(state); !errors.Is(err, ErrInvalidJournal) {
		t.Fatal("schema 1 silently ignores cover", err)
	}
}

func TestCoverRejectsInvalidInputs(t *testing.T) {
	for _, dimensions := range [][2]int{{90, 160}, {160, 91}, {8208, 4617}} {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, dimensions[0], dimensions[1]))); err != nil {
			t.Fatal(err)
		}
		if _, err := validateCover(b.Bytes()); err == nil {
			t.Fatalf("accepted %v", dimensions)
		}
	}
	if _, err := validateCover([]byte("not an image")); err == nil {
		t.Fatal("accepted malformed")
	}
	if _, err := validateCover(make([]byte, 5<<20+1)); err == nil {
		t.Fatal("accepted oversized")
	}
}

func TestCoverFailureReasonDoesNotEchoRemoteContent(t *testing.T) {
	if got := (&CoverProcessingError{Reason: "decode_failed"}).Error(); got != "cover_processing_failed: decode_failed" {
		t.Fatal(got)
	}
	if got := (&CoverProcessingError{Reason: "https://private.invalid/?token=secret"}).Error(); got != "cover_processing_failed" {
		t.Fatal("unsafe diagnostic", got)
	}
}
