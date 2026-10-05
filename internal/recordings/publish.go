package recordings

import (
	"context"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
)

// Publish reuses one durable intent. Replays retain the original If-Match even
// after a human edits or unpublishes the recording; the server receipt wins.
func Publish(ctx context.Context, c *api.Client, j *Journal) (api.PublishResult, error) {
	state := j.State()
	p := c.Principal()
	if !state.Intent.Publish || (state.Intent.Command != "upload" && state.Intent.Command != "publish") || p.Type != state.Intent.PrincipalType || p.ID != state.Intent.PrincipalID || p.ClientID != state.Intent.ClientID {
		return api.PublishResult{}, ErrOperationConflict
	}
	if err := c.RequireScopes("cms:recordings:read", "cms:recordings:publish"); err != nil {
		return api.PublishResult{}, err
	}
	if state.RecordingID == "" {
		state.RecordingID = state.Intent.RecordingID
	}
	if state.PublishExpectedVersion == 0 {
		r, err := retryControl(ctx, func() (api.Recording, error) { return c.GetRecording(ctx, state.RecordingID) })
		if err != nil {
			return api.PublishResult{}, err
		}
		if r.Status != "draft" || r.ReadyAt == nil || r.ExpiresAt == nil || !time.Now().Before(*r.ExpiresAt) || !packageIDPattern.MatchString(r.PackageID) || state.PackageID != "" && r.PackageID != state.PackageID {
			return api.PublishResult{}, ErrOperationConflict
		}
		state.PackageID = r.PackageID
		state.PublishExpectedVersion = r.Version
		if state.Cover != nil && state.Cover.Receipt != nil && r.Version != state.Cover.Receipt.RecordingVersion {
			return api.PublishResult{}, ErrOperationConflict
		}
		if err := j.Save(state); err != nil {
			return api.PublishResult{}, err
		}
	}
	attempt := 0
	value, err := retryControl(ctx, func() (api.PublishResult, error) {
		if attempt > 0 {
			if _, err := c.GetRecording(ctx, state.RecordingID); err != nil {
				return api.PublishResult{}, err
			}
		}
		attempt++
		return c.PublishRecording(ctx, state.RecordingID, state.PublishExpectedVersion, state.OperationID+":publish")
	})
	if err != nil {
		return api.PublishResult{}, err
	}
	if value.Receipt.PackageID != state.PackageID {
		return api.PublishResult{}, api.ErrInvalidResponse
	}
	if value.Outcome != "published" {
		return value, &api.Error{Code: "state_changed"}
	}
	return value, nil
}
