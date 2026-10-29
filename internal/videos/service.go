// Package videos owns read-only video query contracts.
package videos

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrInput = errors.New("invalid_query_input")
var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var hhcID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func ValidYouTubeID(s string) bool { return videoID.MatchString(s) }
func ValidHHCID(s string) bool     { return hhcID.MatchString(s) }

type Input struct {
	Query          string `json:"query,omitempty"`
	Source         string `json:"source,omitempty"`
	ID             string `json:"id,omitempty"`
	YouTubeVideoID string `json:"youtubeVideoId,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	MaxPages       int    `json:"maxPages,omitempty"`
	Locale         string `json:"locale,omitempty"`
}

func (in Input) Normalize() (Input, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.MaxPages == 0 {
		in.MaxPages = 3
	}
	if in.Locale == "" {
		in.Locale = "zh-Hant"
	}
	if in.Limit < 1 || in.Limit > 100 || in.MaxPages < 1 || in.MaxPages > 10 || !slices.Contains([]string{"", "all", "hhc", "youtube"}, in.Source) || !slices.Contains([]string{"zh-Hant", "zh-Hans", "en"}, in.Locale) || len(in.Query) > 512 || !utf8.ValidString(in.Query) || strings.ContainsAny(in.Query, "\x00\r\n") {
		return in, ErrInput
	}
	return in, nil
}

type Video struct {
	ID             string     `json:"id"` // Source-qualified, never a title-derived identity.
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	YouTubeVideoID string     `json:"youtubeVideoId,omitempty"`
	ChannelID      string     `json:"channelId,omitempty"`
	URL            string     `json:"url,omitempty"`
	Publication    string     `json:"publication"`
	Privacy        string     `json:"privacy"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	Duration       string     `json:"duration,omitempty"`
	Provenance     string     `json:"provenance"`
	ObservedAt     time.Time  `json:"observedAt"`
}
type Page struct {
	Items    []Video `json:"items"`
	Complete bool    `json:"complete"`
	Scanned  int     `json:"scanned"`
	Coverage string  `json:"coverage"`
}
type Failure struct {
	CallerAuthority bool   `json:"-"`
	Code            string `json:"code"`
	HTTPStatus      int    `json:"httpStatus,omitempty"`
	Retryable       bool   `json:"retryable"`
}

func (e *Failure) Error() string { return e.Code }
func SafeFailure(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return &Failure{Code: f.Code, HTTPStatus: f.HTTPStatus, Retryable: f.Retryable, CallerAuthority: f.CallerAuthority}
	}
	if errors.Is(err, context.Canceled) {
		return &Failure{Code: "cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{Code: "timeout", Retryable: true}
	}
	return &Failure{Code: "unavailable", Retryable: true}
}
func FailureCode(code string) error {
	return &Failure{Code: code, Retryable: code == "unavailable" || code == "timeout" || code == "rate_limited"}
}

type SourceResult struct {
	Source string `json:"source"`
	Page
	Error *Failure `json:"error,omitempty"`
}
type Result struct {
	Sources       []SourceResult `json:"sources"`
	Incomplete    bool           `json:"incomplete"`
	WebsiteStatus string         `json:"websiteStatus,omitempty"`
	ObservedAt    time.Time      `json:"observedAt"`
}
type Provider interface {
	Search(context.Context, Input) (Page, error)
	Get(context.Context, string, Input) (Page, error)
}

// Service composes bounded source queries independently of CLI parsing.
type Service struct {
	HHC, YouTube Provider
	// SourceTimeout is a trusted composition setting, never a tool argument. Its
	// default/max 20 seconds fits within the outer 30-second command budget.
	SourceTimeout time.Duration
}

func (s Service) Execute(ctx context.Context, tool string, in Input) (Result, error) {
	callerContext := ctx
	sourceTimeout := 20 * time.Second
	if s.SourceTimeout != 0 {
		if s.SourceTimeout < 0 || s.SourceTimeout > sourceTimeout {
			return Result{}, ErrInput
		}
		sourceTimeout = s.SourceTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := Result{Sources: []SourceResult{}, ObservedAt: time.Now().UTC()}
	in, err := in.Normalize()
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	switch tool {
	case "search":
		if in.ID != "" || in.YouTubeVideoID != "" {
			return out, ErrInput
		}
	case "get":
		source, id, ok := strings.Cut(in.ID, ":")
		if !ok || !slices.Contains([]string{"hhc", "youtube"}, source) || in.Source != "" && in.Source != source || in.Query != "" || in.YouTubeVideoID != "" || source == "hhc" && !ValidHHCID(id) || source == "youtube" && !ValidYouTubeID(id) {
			return out, ErrInput
		}
		in.Source = source
	case "compare":
		if !ValidYouTubeID(in.YouTubeVideoID) || in.Query != "" || in.ID != "" || in.Source != "" && in.Source != "all" {
			return out, ErrInput
		}
		in.Source = "all"
	default:
		return out, ErrInput
	}
	type named struct {
		name string
		p    Provider
	}
	selected := []named{}
	for _, n := range []named{{"hhc", s.HHC}, {"youtube", s.YouTube}} {
		if in.Source == "" || in.Source == "all" || in.Source == n.name {
			selected = append(selected, n)
		}
	}
	out.Sources = make([]SourceResult, len(selected))
	var wg sync.WaitGroup
	for i, n := range selected {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sourceContext, sourceCancel := context.WithTimeout(ctx, sourceTimeout)
			defer sourceCancel()
			r := SourceResult{Source: n.name, Page: Page{Items: []Video{}}}
			var err error
			if n.p == nil {
				err = FailureCode("not_configured")
			} else if tool == "get" {
				_, id, _ := strings.Cut(in.ID, ":")
				r.Page, err = n.p.Get(sourceContext, id, in)
			} else if tool == "compare" && n.name == "youtube" {
				r.Page, err = n.p.Get(sourceContext, in.YouTubeVideoID, in)
			} else {
				r.Page, err = n.p.Search(sourceContext, in)
			}
			if sourceContext.Err() != nil && (err == nil || !SafeFailure(err).CallerAuthority) {
				err = sourceContext.Err()
			}
			if err != nil {
				r.Error = SafeFailure(err)
				r.Complete = false
				r.Items = []Video{}
			}
			if r.Items == nil {
				r.Items = []Video{}
			}
			out.Sources[i] = r
		}()
	}
	wg.Wait()
	if err = callerContext.Err(); err != nil {
		return Result{}, err
	}
	for _, r := range out.Sources {
		out.Incomplete = out.Incomplete || !r.Complete || r.Error != nil
	}
	if tool == "compare" {
		out.WebsiteStatus = websiteStatus(out.Sources[0], in.YouTubeVideoID)
	}
	return out, nil
}

func websiteStatus(h SourceResult, id string) string {
	if h.Error != nil {
		return "unknown"
	}
	status := "unknown"
	if h.Complete {
		status = "not_observed"
	}
	unpublished, ambiguous := false, false
	for _, v := range h.Items {
		if v.YouTubeVideoID != id {
			continue
		}
		switch v.Publication {
		case "published":
			return "published"
		case "not_published":
			unpublished = true
		default:
			ambiguous = true
		}
	}
	if ambiguous {
		return "unknown"
	}
	if unpublished {
		return "not_published"
	}
	return status
}
