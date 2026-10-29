// Package hhc queries supported public and human-authorized content routes.
package hhc

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

const PublicOrigin = "https://www.alive.org.tw"
const AdminOrigin = "https://admin.alive.org.tw"

// Grant is injected by a trusted runtime, never supplied by a tool caller.
type Grant struct {
	Human  bool
	Scope  string
	Bearer string
}

func (Grant) String() string     { return "[redacted grant]" }
func (g Grant) GoString() string { return g.String() }

type Provider struct {
	Client       *queryhttp.Client
	Grant        Grant
	PublicClient *queryhttp.Client
}
type wireVideo struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Summary         string     `json:"summary"`
	YouTubeID       string     `json:"youtubeVideoId"`
	Status          string     `json:"status"`
	IsPublished     *bool      `json:"isPublished"`
	PublishedAt     *time.Time `json:"publishedAt"`
	LastPublishedAt *time.Time `json:"lastPublishedAt"`
	Translations    []struct {
		Locale  string `json:"locale"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"translations"`
}
type envelope struct {
	Data []wireVideo `json:"data"`
	Meta struct {
		Page     int `json:"page"`
		PageSize int `json:"pageSize"`
		Total    int `json:"total"`
	} `json:"meta"`
}

func (p *Provider) route() (string, http.Header, error) {
	h := make(http.Header)
	if p.Grant.Bearer == "" {
		if p.Grant.Human || p.Grant.Scope != "" {
			return "", nil, videos.FailureCode("authentication_required")
		}
		return "/api/videos", h, nil
	}
	if !p.Grant.Human || !readScopes(p.Grant.Scope) || strings.ContainsAny(p.Grant.Bearer, "\r\n") {
		return "", nil, videos.FailureCode("permission_denied")
	}
	h.Set("Authorization", "Bearer "+p.Grant.Bearer)
	return "/api/admin/content/videos", h, nil
}
func readScopes(s string) bool {
	found := false
	for _, scope := range strings.Fields(s) {
		if scope == "cms:pages:read" {
			found = true
		} else if scope != "offline_access" {
			return false
		}
	}
	return found
}
func (p *Provider) Search(ctx context.Context, in videos.Input) (videos.Page, error) {
	if in.YouTubeVideoID != "" && p.Grant.Bearer != "" {
		var publicClient *queryhttp.Client
		if p.PublicClient != nil {
			copy := *p.PublicClient
			publicClient = &copy
		}
		if publicClient == nil {
			publicClient = queryhttp.New(PublicOrigin)
		}
		publicClient.Permit = p.Client.Permit
		public := Provider{Client: publicClient}
		observed, err := public.scan(ctx, in, "")
		if err != nil || !observed.Complete || len(observed.Items) > 0 {
			return observed, err
		}
		return p.scan(ctx, in, "")
	}
	return p.scan(ctx, in, "")
}
func (p *Provider) Get(ctx context.Context, id string, in videos.Input) (videos.Page, error) {
	if !videos.ValidHHCID(id) {
		return videos.Page{}, videos.ErrInput
	}
	return p.scan(ctx, in, id)
}
func (p *Provider) scan(ctx context.Context, in videos.Input, id string) (videos.Page, error) {
	out := videos.Page{Items: []videos.Video{}, Coverage: "published_only"}
	path, h, err := p.route()
	if err != nil {
		return out, err
	}
	admin := p.Grant.Bearer != ""
	if admin {
		out.Coverage = "human_content"
	}
	seen := map[string]bool{}
	total := -1
	for page := 1; page <= in.MaxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {"100"}, "locale": {in.Locale}}
		// Exact-ID comparisons scan rather than relying on title-oriented q behavior.
		if admin && in.Query != "" && id == "" && in.YouTubeVideoID == "" {
			q.Set("q", in.Query)
		}
		var e envelope
		if err := p.Client.Get(ctx, path, q, h, "hhc.read", 1, &e); err != nil {
			return out, err
		}
		if e.Data == nil || e.Meta.Page != page || e.Meta.PageSize != 100 || e.Meta.Total < 0 || len(e.Data) > 100 || out.Scanned+len(e.Data) > e.Meta.Total || total >= 0 && e.Meta.Total != total {
			return out, videos.FailureCode("invalid_response")
		}
		total = e.Meta.Total
		for _, w := range e.Data {
			if !videos.ValidHHCID(w.ID) || seen[w.ID] || w.YouTubeID != "" && !videos.ValidYouTubeID(w.YouTubeID) {
				return out, videos.FailureCode("invalid_response")
			}
			seen[w.ID] = true
			out.Scanned++
			v, err := convert(w, admin, in.Locale)
			if err != nil {
				return out, err
			}
			if id != "" && w.ID != id || in.YouTubeVideoID != "" && w.YouTubeID != in.YouTubeVideoID || in.Query != "" && !strings.Contains(strings.ToLower(v.Title+" "+v.Description), strings.ToLower(in.Query)) {
				continue
			}
			if len(out.Items) == in.Limit {
				return out, nil
			}
			out.Items = append(out.Items, v)
		}
		if out.Scanned >= total {
			out.Complete = true
			return out, nil
		}
		if len(e.Data) == 0 {
			return out, videos.FailureCode("invalid_response")
		}
	}
	return out, nil
}
func convert(w wireVideo, admin bool, locale string) (videos.Video, error) {
	v := videos.Video{ID: "hhc:" + w.ID, Title: w.Title, Description: w.Summary, YouTubeVideoID: w.YouTubeID, Publication: "published", Privacy: "public", PublishedAt: w.PublishedAt, Provenance: "hhc.public", ObservedAt: time.Now().UTC()}
	if !admin && v.PublishedAt == nil {
		v.PublishedAt = w.LastPublishedAt
	}
	if admin {
		v.Provenance = "hhc.admin"
		v.Privacy = "unknown"
		v.Publication = "unknown"
		if w.IsPublished == nil {
			return v, videos.FailureCode("invalid_response")
		}
		if *w.IsPublished {
			v.Publication = "unknown"
			v.Privacy = "unknown"
		} else {
			v.Publication = "not_published"
		}
		for i, tr := range w.Translations {
			if i == 0 || tr.Locale == locale {
				v.Title = tr.Title
				v.Description = tr.Summary
			}
			if tr.Locale == locale {
				break
			}
		}
	}
	if len(v.Title) > 4096 || len(v.Description) > 32768 {
		return v, videos.FailureCode("invalid_response")
	}
	if w.YouTubeID != "" {
		v.URL = "https://www.youtube.com/watch?v=" + w.YouTubeID
	}
	return v, nil
}
