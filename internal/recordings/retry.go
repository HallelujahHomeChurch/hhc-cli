package recordings

import (
	"context"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
)

// Only use for reads, ephemeral signing, or mutations whose durable key and
// precondition are already saved. Unknown completion/PUT results are reconciled
// through package status instead of blindly replaying bytes.
func retryControl[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		var zero T
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := call()
		if err == nil || attempt == 2 || !transient(err) {
			return value, err
		}
		if err := waitRetry(ctx, err, attempt); err != nil {
			return zero, err
		}
	}
}

func transient(err error) bool {
	var remote *api.Error
	return errors.As(err, &remote) && (remote.Code == "api_unavailable" || remote.Code == "rate_limited") || errors.Is(err, ErrTransferUnavailable)
}

func waitRetry(ctx context.Context, err error, attempt int) error {
	delay := time.Second * time.Duration(1<<min(attempt, 4))
	var remote *api.Error
	if errors.As(err, &remote) {
		delay = max(delay, remote.RetryAfter)
	}
	return waitContext(ctx, delay)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
