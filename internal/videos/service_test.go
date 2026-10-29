package videos

import (
	"context"
	"errors"
	"testing"
)

type fixture struct {
	page Page
	err  error
}

func (f fixture) Search(context.Context, Input) (Page, error)      { return f.page, f.err }
func (f fixture) Get(context.Context, string, Input) (Page, error) { return f.page, f.err }
func TestPartialFailureAndExactJoin(t *testing.T) {
	s := Service{HHC: fixture{page: Page{Items: []Video{{ID: "hhc:one", YouTubeVideoID: "abcdefghijk", Publication: "published"}}, Complete: true}}, YouTube: fixture{err: errors.New("secret remote body")}}
	r, err := s.Execute(context.Background(), "compare_publication", Input{YouTubeVideoID: "abcdefghijk"})
	if err != nil || !r.Incomplete || r.WebsiteStatus != "published" || len(r.Sources) != 2 || r.Sources[1].Error.Code != "unavailable" {
		t.Fatalf("unexpected result: %+v %v", r, err)
	}
	s.HHC = fixture{page: Page{Items: []Video{{ID: "hhc:one", YouTubeVideoID: "different11", Publication: "published"}}, Complete: true}}
	r, _ = s.Execute(context.Background(), "compare_publication", Input{YouTubeVideoID: "abcdefghijk"})
	if r.WebsiteStatus != "not_observed" {
		t.Fatal(r.WebsiteStatus)
	}
	s.HHC = fixture{page: Page{Complete: false}}
	r, _ = s.Execute(context.Background(), "compare_publication", Input{YouTubeVideoID: "abcdefghijk"})
	if r.WebsiteStatus != "unknown" {
		t.Fatal(r.WebsiteStatus)
	}
}
func TestInputAndCancellation(t *testing.T) {
	s := Service{}
	for _, in := range []Input{{Source: "arbitrary"}, {Limit: 101}, {MaxPages: 11}, {ID: "https://bad"}} {
		if _, err := s.Execute(context.Background(), "get_video", in); !errors.Is(err, ErrInput) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Execute(ctx, "search_videos", Input{}); !errors.Is(err, context.Canceled) {
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
