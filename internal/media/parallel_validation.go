package media

import (
	"context"
	"sync"
)

// Each rendition retains its sequential fragment continuity checks.
func validateRenditions(ctx context.Context, renditions []RecordingRendition, validate func(context.Context, RecordingRendition) (RenditionMedia, error)) ([]RenditionMedia, error) {
	if len(renditions) < 1 || len(renditions) > 3 {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	measured := make([]RenditionMedia, len(renditions))
	var workers sync.WaitGroup
	for i, rendition := range renditions {
		workers.Go(func() {
			if ctx.Err() != nil {
				return
			}
			value, err := validate(ctx, rendition)
			if err != nil {
				cancel(err)
				return
			}
			measured[i] = value
		})
	}
	// Join before callers can finalize or clean the working directory.
	workers.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return measured, nil
}
