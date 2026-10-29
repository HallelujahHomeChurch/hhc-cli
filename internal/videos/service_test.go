package videos

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixture struct {
	page Page
	err  error
}

func (f fixture) Search(context.Context, Input) (Page, error)      { return f.page, f.err }
func (f fixture) Get(context.Context, string, Input) (Page, error) { return f.page, f.err }
func TestPartialFailureAndExactJoin(t *testing.T) {
	s := Service{HHC: fixture{page: Page{Items: []Video{{ID: "hhc:one", YouTubeVideoID: "abcdefghijk", Publication: "published"}}, Complete: true}}, YouTube: fixture{err: errors.New("secret remote body")}}
	r, err := s.Execute(context.Background(), "compare", Input{YouTubeVideoID: "abcdefghijk"})
	if err != nil || !r.Incomplete || r.WebsiteStatus != "published" || len(r.Sources) != 2 || r.Sources[1].Error.Code != "unavailable" {
		t.Fatalf("unexpected result: %+v %v", r, err)
	}
	s.HHC = fixture{page: Page{Items: []Video{{ID: "hhc:one", YouTubeVideoID: "different11", Publication: "published"}}, Complete: true}}
	r, _ = s.Execute(context.Background(), "compare", Input{YouTubeVideoID: "abcdefghijk"})
	if r.WebsiteStatus != "not_observed" {
		t.Fatal(r.WebsiteStatus)
	}
	s.HHC = fixture{page: Page{Complete: false}}
	r, _ = s.Execute(context.Background(), "compare", Input{YouTubeVideoID: "abcdefghijk"})
	if r.WebsiteStatus != "unknown" {
		t.Fatal(r.WebsiteStatus)
	}
}
func TestInputAndCancellation(t *testing.T) {
	s := Service{}
	for _, in := range []Input{{Source: "arbitrary"}, {Limit: 101}, {MaxPages: 11}, {ID: "https://bad"}} {
		if _, err := s.Execute(context.Background(), "get", in); !errors.Is(err, ErrInput) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Execute(ctx, "search", Input{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAmbiguousAdminSnapshotAndPrivateStatus(t *testing.T) {
	for _, tc := range []struct{ publication, want string }{{"unknown", "unknown"}, {"not_published", "not_published"}, {"published", "published"}} {
		h := SourceResult{Page: Page{Complete: true, Items: []Video{{YouTubeVideoID: "abcdefghijk", Publication: tc.publication}}}}
		if got := websiteStatus(h, "abcdefghijk"); got != tc.want {
			t.Fatalf("%s got %s", tc.publication, got)
		}
	}
}

type waitingProvider struct{ started chan struct{} }

func (p waitingProvider) Search(ctx context.Context, _ Input) (Page, error) {
	if p.started != nil {
		close(p.started)
	}
	<-ctx.Done()
	return Page{}, ctx.Err()
}
func (p waitingProvider) Get(ctx context.Context, _ string, in Input) (Page, error) {
	return p.Search(ctx, in)
}
func TestSourceTimeoutPreservesCompletedSource(t *testing.T) {
	s := Service{HHC: fixture{page: Page{Items: []Video{{ID: "hhc:one", YouTubeVideoID: "abcdefghijk", Publication: "published"}}, Complete: true}}, YouTube: waitingProvider{}, SourceTimeout: 20 * time.Millisecond}
	r, err := s.Execute(context.Background(), "compare", Input{YouTubeVideoID: "abcdefghijk"})
	if err != nil || len(r.Sources) != 2 || len(r.Sources[0].Items) != 1 || r.Sources[1].Error == nil || r.Sources[1].Error.Code != "timeout" || !r.Incomplete || r.WebsiteStatus != "published" {
		t.Fatalf("successful source lost: %+v %v", r, err)
	}
}
func TestCallerCancellationDuringSourceRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := (Service{HHC: waitingProvider{started: started}, SourceTimeout: time.Second}).Execute(ctx, "search", Input{Source: "hhc"})
		done <- outcome{r, err}
	}()
	<-started
	cancel()
	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) || len(out.result.Sources) != 0 {
			t.Fatalf("caller cancellation hidden: %+v %v", out.result, out.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller cancellation did not stop query")
	}
}
func TestCallerDeadlineIsNotASourceOnlyFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r, err := (Service{HHC: waitingProvider{}, SourceTimeout: time.Second}).Execute(ctx, "search", Input{Source: "hhc"})
	if !errors.Is(err, context.DeadlineExceeded) || len(r.Sources) != 0 {
		t.Fatalf("caller deadline hidden: %+v %v", r, err)
	}
}

type expiredAuthorityProvider struct{}

func (expiredAuthorityProvider) Search(ctx context.Context, _ Input) (Page, error) {
	<-ctx.Done()
	return Page{}, &Failure{Code: "authentication_required", CallerAuthority: true}
}
func (p expiredAuthorityProvider) Get(ctx context.Context, _ string, in Input) (Page, error) {
	return p.Search(ctx, in)
}
func TestKnownCallerRevocationSurvivesSourceDeadline(t *testing.T) {
	service := Service{HHC: expiredAuthorityProvider{}, SourceTimeout: time.Millisecond}
	r, err := service.Execute(context.Background(), "search", Input{Source: "hhc"})
	if err != nil || len(r.Sources) != 1 || r.Sources[0].Error == nil || !r.Sources[0].Error.CallerAuthority {
		t.Fatal("source timeout erased caller revocation")
	}
}
