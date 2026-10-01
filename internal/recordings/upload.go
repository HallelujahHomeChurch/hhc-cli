package recordings

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

var ErrPackageFailed = errors.New("package_failed")
var ErrSessionExpired = errors.New("session_expired")

type UploadResult struct {
	OperationID              string                   `json:"operationId"`
	Package                  api.Package              `json:"-"`
	Publication              *api.PublishResult       `json:"publication,omitempty"`
	RecordingID              string                   `json:"recordingId,omitempty"`
	PackageID                string                   `json:"packageId,omitempty"`
	PackageDigest            string                   `json:"packageDigest,omitempty"`
	SizeBytes                int64                    `json:"sizeBytes"`
	SourceFingerprint        *media.SourceFingerprint `json:"sourceFingerprint"`
	PrepareState             string                   `json:"prepareState"`
	TransferState            string                   `json:"transferState"`
	ValidationState          string                   `json:"validationState"`
	PublicationState         string                   `json:"publicationState"`
	RequestedActionSatisfied bool                     `json:"requestedActionSatisfied"`
	LocalCleanupState        string                   `json:"localCleanupState"`
	CleanupBytesRemaining    int64                    `json:"cleanupBytesRemaining"`
}

// UploadPrepared also resumes an existing operation. Only remote status decides
// which objects need transfer; a successful PUT or complete response is not ready.
// User-owned input is never cleaned by this function.
func UploadPrepared(ctx context.Context, c *api.Client, u *Uploader, j *Journal) (UploadResult, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 4*time.Hour)
		defer cancel()
	}
	state := j.State()
	result := UploadResult{OperationID: state.OperationID, PrepareState: "not_applicable", TransferState: "unknown", ValidationState: "unknown", PublicationState: "unknown", LocalCleanupState: "not_applicable"}
	p := c.Principal()
	if state.Intent.Command != "upload" || state.Intent.Prepare || p.Type != state.Intent.PrincipalType || p.ID != state.Intent.PrincipalID || p.ClientID != state.Intent.ClientID {
		return result, ErrOperationConflict
	}
	scopes := []string{"cms:recordings:read", "cms:recordings:write"}
	if state.Intent.Publish {
		scopes = append(scopes, "cms:recordings:publish")
	}
	if err := c.RequireScopes(scopes...); err != nil {
		return result, err
	}
	inv, err := media.ReadPackage(ctx, state.Intent.Input)
	if err != nil {
		return result, err
	}
	state.PackageDigest = inv.InventoryDigest
	result.PackageDigest = inv.InventoryDigest
	result.SizeBytes, _ = media.ValidateRecordingInventory(inv)
	if err := j.Save(state); err != nil {
		return result, err
	}
	if state.RecordingID == "" {
		r, err := c.CreateRecording(ctx, state.Intent.Title, state.OperationID+":create")
		if err != nil {
			return result, err
		}
		state.RecordingID = r.ID
		if err := j.Save(state); err != nil {
			return result, err
		}
	}
	if state.PackageID == "" {
		pkg, err := c.CreatePackage(ctx, state.RecordingID, inv, state.OperationID+":package")
		if err != nil {
			return result, err
		}
		state.PackageID, state.SessionID = pkg.PackageID, pkg.SessionID
		if err := j.Save(state); err != nil {
			return result, err
		}
	}
	objects := make(map[string]media.RecordingPackageObject, len(inv.Objects))
	result.RecordingID, result.PackageID = state.RecordingID, state.PackageID
	for _, object := range inv.Objects {
		objects[object.Path] = object
	}
	root, err := os.OpenRoot(state.Intent.Input)
	if err != nil {
		return result, err
	}
	defer root.Close()
	completionAccepted := false
	for {
		pkg, confirmed, err := confirmedPackage(ctx, c, state, objects)
		if err != nil {
			return result, err
		}
		result.Package = pkg
		switch pkg.State {
		case "ready":
			if pkg.MediaExpiresAt == nil || !time.Now().Before(*pkg.MediaExpiresAt) {
				return result, ErrSessionExpired
			}
			result.TransferState, result.ValidationState = "complete", "ready"
			if !state.Intent.Publish {
				result.RequestedActionSatisfied = true
				return result, nil
			}
			if state.PublishExpectedVersion == 0 {
				r, err := c.GetRecording(ctx, state.RecordingID)
				if err != nil {
					return result, err
				}
				if r.PackageID != state.PackageID || r.Status != "draft" {
					return result, ErrOperationConflict
				}
				state.PublishExpectedVersion = r.Version
				if err := j.Save(state); err != nil {
					return result, err
				}
			}
			published, err := c.PublishRecording(ctx, state.RecordingID, state.PublishExpectedVersion, state.OperationID+":publish")
			if err != nil {
				return result, err
			}
			if published.Receipt.PackageID != state.PackageID {
				return result, api.ErrInvalidResponse
			}
			result.Publication = &published
			result.PublicationState = published.Outcome
			if published.Outcome != "published" {
				return result, &api.Error{Code: "state_changed"}
			}
			result.RequestedActionSatisfied = true
			return result, nil
		case "failed":
			result.ValidationState = "failed"
			return result, ErrPackageFailed
		case "expired":
			return result, ErrSessionExpired
		case "uploading":
			result.TransferState = "uploading"
			if completionAccepted {
				return result, api.ErrInvalidResponse
			}
			if !time.Now().Before(pkg.ExpiresAt) {
				return result, ErrSessionExpired
			}
			missing := make([]string, 0, len(objects)-len(confirmed))
			for _, object := range inv.Objects {
				if !confirmed[object.Path] {
					missing = append(missing, object.Path)
				}
			}
			for start := 0; start < len(missing); start += 100 {
				signed, err := c.SignPackage(ctx, state.RecordingID, state.PackageID, missing[start:min(start+100, len(missing))])
				if err != nil {
					return result, err
				}
				if err := uploadBatch(ctx, u, root, state.PackageID, objects, signed); err != nil {
					return result, err
				}
			}
			if _, err := c.CompletePackage(ctx, state.RecordingID, state.PackageID); err != nil {
				return result, err
			}
			completionAccepted = true
			continue // Query immediately; completion itself never establishes ready.
		case "freezing", "validating":
			result.TransferState = "complete"
			result.ValidationState = pkg.State
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		default:
			return result, api.ErrInvalidResponse
		}
	}
}

func confirmedPackage(ctx context.Context, c *api.Client, state JournalState, objects map[string]media.RecordingPackageObject) (api.Package, map[string]bool, error) {
	confirmed := make(map[string]bool)
	cursor := ""
	for {
		pkg, err := c.PackageStatus(ctx, state.RecordingID, state.PackageID, cursor)
		if err != nil {
			return api.Package{}, nil, err
		}
		if pkg.State != "uploading" {
			return pkg, nil, nil
		}
		for _, path := range pkg.ConfirmedObjects {
			if _, exists := objects[path]; !exists || confirmed[path] {
				return api.Package{}, nil, api.ErrInvalidResponse
			}
			confirmed[path] = true
		}
		if pkg.NextCursor == "" {
			return pkg, confirmed, nil
		}
		_, knownCursor := objects[pkg.NextCursor]
		if !knownCursor || pkg.NextCursor <= cursor || len(confirmed) > len(objects) {
			return api.Package{}, nil, api.ErrInvalidResponse
		}
		cursor = pkg.NextCursor
	}
}

func uploadBatch(ctx context.Context, u *Uploader, root *os.Root, pkg string, objects map[string]media.RecordingPackageObject, signed []api.SignedObject) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan api.SignedObject)
	errorsOut := make(chan error, 3)
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() {
			for target := range jobs {
				info, err := root.Lstat(target.Path)
				if err == nil && !info.Mode().IsRegular() {
					err = ErrPackageChanged
				}
				if err == nil {
					var f *os.File
					f, err = root.Open(target.Path)
					if err == nil {
						opened, statErr := f.Stat()
						if statErr != nil || !os.SameFile(info, opened) {
							err = ErrPackageChanged
						} else {
							err = u.Put(ctx, f, pkg, objects[target.Path], target)
						}
						f.Close()
					}
				}
				if err != nil {
					errorsOut <- err
					cancel()
					return
				}
			}
		})
	}
dispatch:
	for _, target := range signed {
		select {
		case jobs <- target:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return ctx.Err()
}
