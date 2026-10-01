package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

var packageIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var operationKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
var objectPathPattern = regexp.MustCompile(`^(master\.m3u8|(720p|1080p)/(index\.m3u8|init\.mp4|seg-[0-9]{6}\.m4s))$`)

type PublishReceipt struct {
	OperationKey     string    `json:"operationKey"`
	ActorType        string    `json:"actorType"`
	ActorID          string    `json:"actorId"`
	RecordingID      string    `json:"recordingId"`
	PackageID        string    `json:"packageId"`
	ExpectedVersion  int64     `json:"expectedVersion"`
	PublishedVersion int64     `json:"publishedVersion"`
	PublishedAt      time.Time `json:"publishedAt"`
}

type PublishResult struct {
	Receipt PublishReceipt `json:"receipt"`
	Current Recording      `json:"current"`
	Outcome string         `json:"outcome"`
}

func (c *Client) PublishRecording(ctx context.Context, id string, version int64, key string) (PublishResult, error) {
	if !ValidRecordingID(id) || version < 1 || !operationKeyPattern.MatchString(key) {
		return PublishResult{}, auth.ErrInvalidAuthInput
	}
	var value PublishResult
	err := c.request(ctx, http.MethodPost, "/api/admin/recordings/"+id+"/publish", "cms:recordings:publish", []byte(`{}`), http.Header{"Idempotency-Key": {key}, "If-Match": {`"` + strconv.FormatInt(version, 10) + `"`}}, &value)
	if err != nil {
		return PublishResult{}, err
	}
	p := c.Principal()
	r := value.Receipt
	if r.OperationKey != key || r.ActorType != p.Type || r.ActorID != p.ID || r.RecordingID != id || !packageIDPattern.MatchString(r.PackageID) || r.ExpectedVersion != version || r.PublishedVersion <= version || r.PublishedAt.IsZero() || value.Current.ID != id || value.Current.Version < r.PublishedVersion || !slices.Contains([]string{"published", "state_changed"}, value.Outcome) {
		return PublishResult{}, ErrInvalidResponse
	}
	if value.Outcome == "published" && (value.Current.Status != "published" || value.Current.Hidden || value.Current.PackageID != r.PackageID || value.Current.Version != r.PublishedVersion || value.Current.ReadyAt == nil || value.Current.ExpiresAt == nil || !time.Now().Before(*value.Current.ExpiresAt)) {
		return PublishResult{}, ErrInvalidResponse
	}
	return value, nil
}

// SignedObject is an ephemeral capability, never a journal or command result.
type SignedObject struct {
	Path    string      `json:"path"`
	URL     string      `json:"url"`
	Method  string      `json:"method"`
	Headers http.Header `json:"headers"`
}

func (SignedObject) String() string     { return "[redacted upload capability]" }
func (s SignedObject) GoString() string { return s.String() }
func (SignedObject) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted upload capability]"`), nil
}

func (c *Client) SignPackage(ctx context.Context, recordingID, packageID string, paths []string) ([]SignedObject, error) {
	if !ValidRecordingID(recordingID) || !packageIDPattern.MatchString(packageID) || len(paths) < 1 || len(paths) > 100 {
		return nil, auth.ErrInvalidAuthInput
	}
	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		if !objectPathPattern.MatchString(path) || wanted[path] {
			return nil, auth.ErrInvalidAuthInput
		}
		wanted[path] = true
	}
	body, _ := json.Marshal(map[string][]string{"paths": paths})
	var value []SignedObject
	if err := c.request(ctx, http.MethodPost, packagePath(recordingID, packageID)+"/sign", "cms:recordings:write", body, nil, &value); err != nil {
		return nil, err
	}
	if len(value) != len(paths) {
		return nil, ErrInvalidResponse
	}
	for _, signed := range value {
		if !wanted[signed.Path] || signed.Method != "PUT" || signed.URL == "" {
			return nil, ErrInvalidResponse
		}
		delete(wanted, signed.Path)
	}
	// The transfer boundary validates origin, expiry, headers and exact object key.
	return value, nil
}

// Package state is authoritative only when returned by the server, never from a PUT.
type Package struct {
	PackageID        string                          `json:"packageId"`
	SessionID        string                          `json:"sessionId"`
	RecordingID      string                          `json:"recordingId"`
	State            string                          `json:"state"`
	ExpiresAt        time.Time                       `json:"expiresAt"`
	ReadyAt          *time.Time                      `json:"readyAt,omitempty"`
	MediaExpiresAt   *time.Time                      `json:"mediaExpiresAt,omitempty"`
	Inventory        media.RecordingPackageInventory `json:"inventory"`
	ConfirmedObjects []string                        `json:"confirmedObjects"`
	NextCursor       string                          `json:"nextCursor"`
}

func (c *Client) CreateRecording(ctx context.Context, title, key string) (Recording, error) {
	if !operationKeyPattern.MatchString(key) || !utf8.ValidString(title) || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 180 {
		return Recording{}, auth.ErrInvalidAuthInput
	}
	body, _ := json.Marshal(map[string]string{"title": strings.TrimSpace(title)})
	var value Recording
	err := c.request(ctx, http.MethodPost, "/api/admin/recordings", "cms:recordings:write", body, http.Header{"Idempotency-Key": {key}}, &value, http.StatusCreated)
	if err != nil {
		return Recording{}, err
	}
	if !ValidRecordingID(value.ID) || value.Version < 1 || value.Status != "draft" {
		return Recording{}, ErrInvalidResponse
	}
	return value, nil
}

func (c *Client) CreatePackage(ctx context.Context, recordingID string, inventory media.RecordingPackageInventory, key string) (Package, error) {
	if !ValidRecordingID(recordingID) || !operationKeyPattern.MatchString(key) || inventory.InventoryDigest == "" {
		return Package{}, auth.ErrInvalidAuthInput
	}
	if _, err := media.ValidateRecordingInventory(inventory); err != nil {
		return Package{}, err
	}
	body, err := json.Marshal(inventory)
	if err != nil {
		return Package{}, auth.ErrInvalidAuthInput
	}
	var value Package
	err = c.request(ctx, http.MethodPost, "/api/admin/recordings/"+recordingID+"/packages", "cms:recordings:write", body, http.Header{"Idempotency-Key": {key}}, &value, http.StatusCreated)
	if err != nil {
		return Package{}, err
	}
	if !validPackage(value, recordingID, value.PackageID) || value.Inventory.InventoryDigest != inventory.InventoryDigest {
		return Package{}, ErrInvalidResponse
	}
	return value, nil
}

func (c *Client) PackageStatus(ctx context.Context, recordingID, packageID, cursor string) (Package, error) {
	if !ValidRecordingID(recordingID) || !packageIDPattern.MatchString(packageID) || len(cursor) > 128 || !utf8.ValidString(cursor) {
		return Package{}, auth.ErrInvalidAuthInput
	}
	query := url.Values{"cursor": {cursor}, "limit": {"1000"}}
	var value Package
	err := c.request(ctx, http.MethodGet, packagePath(recordingID, packageID)+"?"+query.Encode(), "cms:recordings:read", nil, nil, &value)
	if err != nil {
		return Package{}, err
	}
	if !validPackage(value, recordingID, packageID) || len(value.ConfirmedObjects) > 1000 || len(value.NextCursor) > 128 {
		return Package{}, ErrInvalidResponse
	}
	return value, nil
}

func (c *Client) CompletePackage(ctx context.Context, recordingID, packageID string) (Package, error) {
	if !ValidRecordingID(recordingID) || !packageIDPattern.MatchString(packageID) {
		return Package{}, auth.ErrInvalidAuthInput
	}
	var value Package
	err := c.request(ctx, http.MethodPost, packagePath(recordingID, packageID)+"/complete", "cms:recordings:write", []byte(`{}`), nil, &value, http.StatusAccepted)
	if err != nil {
		return Package{}, err
	}
	if !validPackage(value, recordingID, packageID) {
		return Package{}, ErrInvalidResponse
	}
	return value, nil
}

func packagePath(recordingID, packageID string) string {
	return "/api/admin/recordings/" + recordingID + "/packages/" + packageID
}

func validPackage(value Package, recordingID, packageID string) bool {
	if !packageIDPattern.MatchString(packageID) || value.PackageID != packageID || value.SessionID != packageID || value.RecordingID != recordingID || value.ExpiresAt.IsZero() || !slices.Contains([]string{"uploading", "freezing", "validating", "ready", "failed", "expired"}, value.State) {
		return false
	}
	return value.State != "ready" || value.ReadyAt != nil && !value.ReadyAt.IsZero() && value.MediaExpiresAt != nil && value.MediaExpiresAt.Equal(value.ReadyAt.Add(30*24*time.Hour))
}
