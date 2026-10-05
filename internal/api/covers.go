package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
)

var coverID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

type CoverItem struct {
	Error        string `json:"error,omitempty"`
	OperationKey string `json:"operationKey,omitempty"`
	ID           string `json:"id"`
	UploadID     string `json:"uploadId"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
}
type CoverList struct {
	Items            []CoverItem `json:"items"`
	SelectedCoverID  string      `json:"selectedCoverId"`
	RecordingVersion int64       `json:"recordingVersion"`
}
type CoverUpload struct {
	UploadID string `json:"uploadId"`
	State    string `json:"state"`
}
type CoverReceipt struct {
	CoverID          string `json:"coverId"`
	RecordingVersion int64  `json:"recordingVersion"`
}
type CoverSelection struct {
	Recording Recording    `json:"recording"`
	CoverID   string       `json:"coverId"`
	Outcome   string       `json:"outcome"`
	Receipt   CoverReceipt `json:"receipt"`
}

func (c *Client) ListCovers(ctx context.Context, id string) (CoverList, error) {
	if !ValidRecordingID(id) {
		return CoverList{}, auth.ErrInvalidAuthInput
	}
	var value CoverList
	if err := c.request(ctx, http.MethodGet, "/api/admin/recordings/"+id+"/covers", "cms:recordings:read", nil, nil, &value); err != nil {
		return value, err
	}
	if value.RecordingVersion < 1 || len(value.Items) > 1000 || value.SelectedCoverID != "" && !coverID.MatchString(value.SelectedCoverID) {
		return CoverList{}, ErrInvalidResponse
	}
	for _, item := range value.Items {
		if item.ID != "" && !coverID.MatchString(item.ID) || item.UploadID != "" && !coverID.MatchString(item.UploadID) {
			return CoverList{}, ErrInvalidResponse
		}
	}
	return value, nil
}
func (c *Client) UploadCover(ctx context.Context, id string, data []byte, contentType, key string) (CoverUpload, error) {
	if !ValidRecordingID(id) || len(data) == 0 || len(data) > 5<<20 || (contentType != "image/jpeg" && contentType != "image/png") || !operationKeyPattern.MatchString(key) {
		return CoverUpload{}, auth.ErrInvalidAuthInput
	}
	headers := http.Header{"Content-Type": {contentType}, "Idempotency-Key": {key}}
	var value CoverUpload
	if err := c.request(ctx, http.MethodPost, "/api/admin/recordings/"+id+"/cover-uploads", "cms:recordings:write", data, headers, &value, 202); err != nil {
		return value, err
	}
	if !coverID.MatchString(value.UploadID) || (value.State != "pending" && value.State != "processing" && value.State != "ready") {
		return CoverUpload{}, ErrInvalidResponse
	}
	return value, nil
}
func (c *Client) SelectCover(ctx context.Context, id, uploadID string, version int64, key string) (CoverSelection, error) {
	if !ValidRecordingID(id) || !coverID.MatchString(uploadID) || version < 1 || !operationKeyPattern.MatchString(key) {
		return CoverSelection{}, auth.ErrInvalidAuthInput
	}
	body, _ := json.Marshal(struct {
		Mode     string `json:"mode"`
		UploadID string `json:"uploadId"`
	}{"custom", uploadID})
	headers := http.Header{"If-Match": {strconv.Quote(strconv.FormatInt(version, 10))}, "Idempotency-Key": {key}}
	var value CoverSelection
	if err := c.request(ctx, http.MethodPut, "/api/admin/recordings/"+id+"/cover", "cms:recordings:write", body, headers, &value); err != nil {
		return value, err
	}
	if value.Recording.ID != id || value.Recording.Version < 1 || !coverID.MatchString(value.CoverID) || !coverID.MatchString(value.Receipt.CoverID) || value.Receipt.RecordingVersion < 1 || (value.Outcome != "selected" && value.Outcome != "state_changed") {
		return CoverSelection{}, ErrInvalidResponse
	}
	return value, nil
}
