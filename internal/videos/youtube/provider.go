package youtube

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

const Origin = "https://www.googleapis.com"
const ReadScope = "https://www.googleapis.com/auth/youtube.readonly"
const EditScope = "https://www.googleapis.com/auth/youtube.force-ssl"
const ManageScope = "https://www.googleapis.com/auth/youtube"

func EditableScope(scope string) bool { return scope == EditScope || scope == ManageScope }

func QueryScope(scope string) bool { return scope == ReadScope || EditableScope(scope) }

var channelPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

func ValidChannel(id string) bool { return channelPattern.MatchString(id) }

type Grant struct {
	APIKey    string
	Bearer    string
	Scope     string
	ChannelID string
}

func (Grant) String() string     { return "[redacted grant]" }
func (g Grant) GoString() string { return g.String() }

type Provider struct {
	Client *queryhttp.Client
	Grant  Grant
}
type item struct {
	ID      string `json:"id"`
	Snippet struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		ChannelID   string     `json:"channelId"`
		PublishedAt *time.Time `json:"publishedAt"`
	} `json:"snippet"`
	Status struct {
		Privacy string `json:"privacyStatus"`
	} `json:"status"`
	Details struct {
		Duration string `json:"duration"`
	} `json:"contentDetails"`
}

func (p *Provider) headers() (http.Header, error) {
	h := make(http.Header)
	if !ValidChannel(p.Grant.ChannelID) {
		return nil, videos.FailureCode("not_configured")
	}
	if p.Grant.APIKey != "" && p.Grant.Bearer != "" {
		return nil, videos.FailureCode("permission_denied")
	}
	if p.Grant.Bearer != "" {
		if !QueryScope(p.Grant.Scope) || strings.ContainsAny(p.Grant.Bearer, "\r\n") {
			return nil, videos.FailureCode("permission_denied")
		}
		h.Set("Authorization", "Bearer "+p.Grant.Bearer)
	} else if p.Grant.APIKey != "" && !strings.ContainsAny(p.Grant.APIKey, "\r\n") {
		h.Set("X-Goog-Api-Key", p.Grant.APIKey)
	} else {
		return nil, videos.FailureCode("not_configured")
	}
	return h, nil
}
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
func (p *Provider) Get(ctx context.Context, id string, in videos.Input) (videos.Page, error) {
	if !videos.ValidYouTubeID(id) {
		return videos.Page{}, videos.ErrInput
	}
	return p.details(ctx, []string{id}, true)
}
func (p *Provider) details(ctx context.Context, ids []string, complete bool) (videos.Page, error) {
	out := videos.Page{Items: []videos.Video{}, Complete: complete, Coverage: "configured_channel"}
	h, err := p.headers()
	if err != nil {
		return out, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	var e struct {
		Items []item `json:"items"`
	}
	if err = p.Client.Get(ctx, "/youtube/v3/videos", url.Values{"part": {"snippet,status,contentDetails"}, "id": {strings.Join(ids, ",")}}, h, "youtube.default", 1, &e); err != nil {
		return out, err
	}
	if e.Items == nil || len(e.Items) > len(ids) {
		return out, videos.FailureCode("invalid_response")
	}
	seen := map[string]bool{}
	for _, w := range e.Items {
		if !contains(ids, w.ID) || seen[w.ID] || !ValidChannel(w.Snippet.ChannelID) {
			return out, videos.FailureCode("invalid_response")
		}
		seen[w.ID] = true
		// A caller's credential alone does not authorize every channel it can see.
		if w.Snippet.ChannelID != p.Grant.ChannelID {
			return out, videos.FailureCode("resource_denied")
		}
		if !contains([]string{"public", "unlisted", "private"}, w.Status.Privacy) || p.Grant.Bearer == "" && w.Status.Privacy != "public" {
			return out, videos.FailureCode("resource_denied")
		}
		if len(w.Snippet.Title) > 4096 || len(w.Snippet.Description) > 32768 {
			return out, videos.FailureCode("invalid_response")
		}
		out.Items = append(out.Items, videos.Video{ID: "youtube:" + w.ID, YouTubeVideoID: w.ID, Title: w.Snippet.Title, Description: w.Snippet.Description, ChannelID: w.Snippet.ChannelID, URL: "https://www.youtube.com/watch?v=" + w.ID, Publication: "unknown", Privacy: w.Status.Privacy, PublishedAt: w.Snippet.PublishedAt, Duration: w.Details.Duration, Provenance: "youtube.data_api", ObservedAt: time.Now().UTC()})
	}
	out.Scanned = len(ids)
	return out, nil
}
func (p *Provider) Search(ctx context.Context, in videos.Input) (videos.Page, error) {
	out := videos.Page{Items: []videos.Video{}, Coverage: "channel_search_index"}
	h, err := p.headers()
	if err != nil {
		return out, err
	}
	token := ""
	seenTokens := map[string]bool{}
	seenIDs := map[string]bool{}
	for page := 0; page < in.MaxPages; page++ {
		q := url.Values{"part": {"snippet"}, "type": {"video"}, "channelId": {p.Grant.ChannelID}, "maxResults": {"50"}, "q": {in.Query}, "pageToken": {token}}
		// OAuth search is restricted to the owner's approved channel.
		if p.Grant.Bearer != "" {
			q.Set("forMine", "true")
		}
		var e struct {
			Items []struct {
				ID struct {
					VideoID string `json:"videoId"`
				} `json:"id"`
			} `json:"items"`
			Next string `json:"nextPageToken"`
		}
		if err = p.Client.Get(ctx, "/youtube/v3/search", q, h, "youtube.search", 1, &e); err != nil {
			return out, err
		}
		if e.Items == nil || len(e.Items) > 50 || len(e.Next) > 512 || e.Next != "" && seenTokens[e.Next] {
			return out, videos.FailureCode("invalid_response")
		}
		ids := []string{}
		trimmed := false
		for _, v := range e.Items {
			id := v.ID.VideoID
			if !videos.ValidYouTubeID(id) {
				return out, videos.FailureCode("invalid_response")
			}
			if seenIDs[id] {
				continue
			}
			seenIDs[id] = true
			if len(out.Items)+len(ids) >= in.Limit {
				trimmed = true
				break
			}
			ids = append(ids, id)
		}
		details, err := p.details(ctx, ids, false)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, details.Items...)
		out.Scanned += len(e.Items)
		if e.Next == "" && !trimmed {
			out.Complete = true
			return out, nil
		}
		if trimmed || len(out.Items) >= in.Limit {
			return out, nil
		}
		token = e.Next
		seenTokens[token] = true
	}
	return out, nil
}
