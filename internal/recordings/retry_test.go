package recordings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
)

func TestControlRetryIsBoundedAndDoesNotRetryPermissionOrConflict(t *testing.T) {
	for _, failure := range []error{auth.ErrPermissionDenied, &api.Error{Code: "state_changed"}, &api.Error{Code: "operation_conflict"}} {
		calls := 0
		_, err := retryControl(context.Background(), func() (int, error) { calls++; return 0, failure })
		if calls != 1 || !errors.Is(err, failure) {
			t.Fatalf("unsafe retry calls=%d err=%v", calls, err)
		}
	}
	calls := 0
	_, err := retryControl(context.Background(), func() (int, error) { calls++; return 0, &api.Error{Code: "api_unavailable"} })
	if calls != 3 || err == nil {
		t.Fatalf("unbounded retry: %d %v", calls, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	calls = 0
	_, err = retryControl(ctx, func() (int, error) { calls++; return 0, &api.Error{Code: "rate_limited", RetryAfter: time.Hour} })
	if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ignored Retry-After/deadline: %d %v", calls, err)
	}
}
