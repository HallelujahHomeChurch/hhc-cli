package media

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateRenditionsConcurrentAndOrdered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan string, 3)
	release := make(chan struct{})
	renditions := []RecordingRendition{{Name: "480p"}, {Name: "720p"}, {Name: "1080p"}}
	go func() {
		for range renditions {
			select {
			case <-started:
			case <-ctx.Done():
				return
			}
		}
		close(release)
	}()
	got, err := validateRenditions(ctx, renditions, func(ctx context.Context, r RecordingRendition) (RenditionMedia, error) {
		started <- r.Name
		select {
		case <-release:
			return RenditionMedia{Rendition: r}, nil
		case <-ctx.Done():
			return RenditionMedia{}, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range got {
		if r.Rendition.Name != renditions[i].Name {
			t.Fatalf("order: %+v", got)
		}
	}
}

func TestValidateRenditionsFailureCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failure := &ValidationFailure{rendition: "480p", segment: 7, check: "fragment_continuity", cause: ErrInvalidInput}
	var started sync.WaitGroup
	started.Add(3)
	var exited atomic.Int32
	got, err := validateRenditions(ctx, []RecordingRendition{{Name: "480p"}, {Name: "720p"}, {Name: "1080p"}}, func(ctx context.Context, r RecordingRendition) (RenditionMedia, error) {
		started.Done()
		started.Wait()
		if r.Name == "480p" {
			return RenditionMedia{}, failure
		}
		defer exited.Add(1)
		<-ctx.Done()
		return RenditionMedia{}, ctx.Err()
	})
	if got != nil || err != failure {
		t.Fatalf("lost original failure: %v %v", got, err)
	}
	if exited.Load() != 2 {
		t.Fatal("returned before cancelled validators exited")
	}
}

func TestValidateRenditionsBoundsAndCancellation(t *testing.T) {
	for _, count := range []int{0, 4} {
		_, err := validateRenditions(context.Background(), make([]RecordingRendition, count), func(context.Context, RecordingRendition) (RenditionMedia, error) {
			panic("invalid count invoked validation")
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := validateRenditions(ctx, []RecordingRendition{{Name: "480p"}}, func(context.Context, RecordingRendition) (RenditionMedia, error) {
		panic("cancelled validation started")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestValidationFailureNames480p(t *testing.T) {
	_, err := MeasureRendition(context.Background(), "unused", "relative", RecordingRendition{Name: "480p"})
	var failure *ValidationFailure
	if !errors.As(err, &failure) || failure.rendition != "480p" {
		t.Fatalf("missing rendition: %v", err)
	}
}
