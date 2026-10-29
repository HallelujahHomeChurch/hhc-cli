package youtube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

// Patch exposes only the three metadata fields implemented by this CLI.
// A nil field preserves its value; an empty description or tags clears it.
type Patch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
}

func (p Patch) Validate() error {
	if p.Title == nil && p.Description == nil && p.Tags == nil {
		return videos.ErrInput
	}
	if p.Title != nil && (!utf8.ValidString(*p.Title) || strings.TrimSpace(*p.Title) == "" || utf8.RuneCountInString(*p.Title) > 100 || strings.ContainsAny(*p.Title, "<>\r\n\x00")) {
		return videos.ErrInput
	}
	if p.Description != nil && (!utf8.ValidString(*p.Description) || len(*p.Description) > 5000 || strings.ContainsAny(*p.Description, "<>\x00")) {
		return videos.ErrInput
	}
	if p.Tags != nil {
		size := max(0, len(*p.Tags)-1)
		for _, tag := range *p.Tags {
			if !utf8.ValidString(tag) || strings.TrimSpace(tag) == "" || strings.ContainsAny(tag, "\r\n\x00") {
				return videos.ErrInput
			}
			size += utf8.RuneCountInString(tag)
			if strings.Contains(tag, " ") {
				size += 2
			}
		}
		if size > 500 {
			return videos.ErrInput
		}
	}
	return nil
}

type metadataSnippet struct {
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Tags                 []string `json:"tags"`
	CategoryID           string   `json:"categoryId"`
	DefaultLanguage      string   `json:"defaultLanguage,omitempty"`
	DefaultAudioLanguage string   `json:"defaultAudioLanguage,omitempty"`
}
type metadataSnapshot struct {
	id, channel, etag string
	snippet           metadataSnippet
}
type Change struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}
type EditPlan struct {
	ID            string   `json:"id"`
	ChannelID     string   `json:"channelId"`
	ETag          string   `json:"etag"`
	PlanHash      string   `json:"planHash"`
	Changes       []Change `json:"changes"`
	before, after metadataSnippet
}
type EditResult struct {
	DryRun bool `json:"dryRun"`
	EditPlan
	Outcome                  string `json:"outcome"`
	RequestedActionSatisfied bool   `json:"requestedActionSatisfied"`
}

func etagHeader(etag string) (string, error) {
	if etag == "" || len(etag) > 256 || strings.HasPrefix(etag, "W/") || strings.ContainsAny(etag, "\r\n\x00,\\") {
		return "", videos.FailureCode("invalid_response")
	}
	inner := etag
	if strings.HasPrefix(etag, "\"") && strings.HasSuffix(etag, "\"") && len(etag) > 2 {
		inner = etag[1 : len(etag)-1]
	}
	for _, c := range []byte(inner) {
		if c < 33 || c > 126 || c == '"' {
			return "", videos.FailureCode("invalid_response")
		}
	}
	return "\"" + inner + "\"", nil
}

func (p *Provider) editSnapshot(ctx context.Context, id string) (metadataSnapshot, error) {
	if !videos.ValidYouTubeID(id) {
		return metadataSnapshot{}, videos.ErrInput
	}
	if p.Grant.Bearer == "" || !EditableScope(p.Grant.Scope) {
		return metadataSnapshot{}, videos.FailureCode("permission_denied")
	}
	h, err := p.headers()
	if err != nil {
		return metadataSnapshot{}, err
	}
	var response struct {
		Items []struct {
			ID      string          `json:"id"`
			ETag    string          `json:"etag"`
			Snippet json.RawMessage `json:"snippet"`
		} `json:"items"`
	}
	if err = p.Client.Get(ctx, "/youtube/v3/videos", url.Values{"part": {"snippet"}, "id": {id}}, h, "youtube.default", 1, &response); err != nil {
		return metadataSnapshot{}, err
	}
	if len(response.Items) == 0 {
		return metadataSnapshot{}, videos.FailureCode("not_found")
	}
	if len(response.Items) != 1 || response.Items[0].ID != id {
		return metadataSnapshot{}, videos.FailureCode("invalid_response")
	}
	w := response.Items[0]
	if _, err = etagHeader(w.ETag); err != nil {
		return metadataSnapshot{}, err
	}
	var raw map[string]json.RawMessage
	var identity struct {
		ChannelID string `json:"channelId"`
	}
	var snippet metadataSnippet
	if json.Unmarshal(w.Snippet, &raw) != nil || json.Unmarshal(w.Snippet, &identity) != nil || json.Unmarshal(w.Snippet, &snippet) != nil || raw == nil {
		return metadataSnapshot{}, videos.FailureCode("invalid_response")
	}
	if identity.ChannelID != p.Grant.ChannelID {
		return metadataSnapshot{}, videos.FailureCode("resource_denied")
	}
	// Carry every known writable snippet property. Unknown properties stop the
	// edit rather than risking deletion of a newly introduced writable field.
	for key := range raw {
		switch key {
		case "title", "description", "tags", "categoryId", "defaultLanguage", "defaultAudioLanguage", "channelId", "channelTitle", "publishedAt", "thumbnails", "liveBroadcastContent", "localized":
		default:
			return metadataSnapshot{}, videos.FailureCode("unsupported_metadata")
		}
	}
	category, err := strconv.Atoi(snippet.CategoryID)
	if err != nil || category <= 0 || strings.TrimSpace(snippet.Title) == "" {
		return metadataSnapshot{}, videos.FailureCode("invalid_response")
	}
	if snippet.Tags == nil {
		snippet.Tags = []string{}
	}
	return metadataSnapshot{id: id, channel: identity.ChannelID, etag: w.ETag, snippet: snippet}, nil
}

func (p *Provider) PlanEdit(ctx context.Context, id string, patch Patch) (EditPlan, error) {
	if err := patch.Validate(); err != nil {
		return EditPlan{}, err
	}
	current, err := p.editSnapshot(ctx, id)
	if err != nil {
		return EditPlan{}, err
	}
	next := current.snippet
	changes := []Change{}
	if patch.Title != nil && *patch.Title != next.Title {
		changes = append(changes, Change{"title", next.Title, *patch.Title})
		next.Title = *patch.Title
	}
	if patch.Description != nil && *patch.Description != next.Description {
		changes = append(changes, Change{"description", next.Description, *patch.Description})
		next.Description = *patch.Description
	}
	if patch.Tags != nil && !reflect.DeepEqual(*patch.Tags, next.Tags) {
		tags := append([]string{}, (*patch.Tags)...)
		changes = append(changes, Change{"tags", next.Tags, tags})
		next.Tags = tags
	}
	plan := EditPlan{ID: "youtube:" + id, ChannelID: current.channel, ETag: current.etag, Changes: changes, before: current.snippet, after: next}
	canonical, _ := json.Marshal(struct {
		ID, Channel, ETag string
		Snippet           metadataSnippet
	}{id, current.channel, current.etag, next})
	hash := sha256.Sum256(canonical)
	plan.PlanHash = hex.EncodeToString(hash[:])
	return plan, nil
}

// ApplyEdit re-reads the snapshot and requires the exact prior dry-run plan.
// A PUT is attempted once. Uncertain outcomes require read-only reconciliation.
func (p *Provider) ApplyEdit(ctx context.Context, id string, patch Patch, etag, planHash string) (EditResult, error) {
	if etag == "" || len(planHash) != 64 {
		return EditResult{}, videos.ErrInput
	}
	plan, err := p.PlanEdit(ctx, id, patch)
	if err != nil {
		return EditResult{}, err
	}
	out := EditResult{EditPlan: plan, Outcome: "not_applied"}
	if plan.ETag != etag || plan.PlanHash != planHash {
		return out, videos.FailureCode("state_changed")
	}
	if len(plan.Changes) == 0 {
		out.Outcome = "unchanged"
		out.RequestedActionSatisfied = true
		return out, nil
	}
	headers, err := p.headers()
	if err != nil {
		return out, err
	}
	match, err := etagHeader(etag)
	if err != nil {
		return out, err
	}
	body, _ := json.Marshal(struct {
		ID      string          `json:"id"`
		ETag    string          `json:"etag"`
		Snippet metadataSnippet `json:"snippet"`
	}{id, etag, plan.after})
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if p.Client.Permit != nil {
		if err = p.Client.Permit(ctx, "youtube.default", 50); err != nil {
			return out, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.Client.Base+"/youtube/v3/videos?part=snippet", bytes.NewReader(body))
	if err != nil {
		return out, videos.FailureCode("invalid_response")
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("If-Match", match)
	resp, err := p.Client.HTTP.Do(req)
	if err != nil {
		out.Outcome = "uncertain"
		return out, videos.FailureCode("mutation_uncertain")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		code := "mutation_uncertain"
		switch resp.StatusCode {
		case 400:
			code = "invalid_metadata"
		case 401:
			code = "authentication_required"
		case 403:
			code = "permission_denied"
		case 404:
			code = "not_found"
		case 409, 412:
			code = "state_changed"
		case 429:
			code = "rate_limited"
		}
		if code == "mutation_uncertain" {
			out.Outcome = "uncertain"
		}
		return out, &videos.Failure{Code: code, HTTPStatus: resp.StatusCode}
	}
	// A valid write response is followed by an independent current GET. A lost,
	// malformed or unverifiable response cannot authorize another PUT.
	mediaType, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	_ = resp.Body.Close()
	var accepted struct {
		ID string `json:"id"`
	}
	if parseErr != nil || mediaType != "application/json" || readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &accepted) != nil || accepted.ID != id {
		out.Outcome = "uncertain"
		return out, videos.FailureCode("mutation_uncertain")
	}
	observed, err := p.editSnapshot(ctx, id)
	if err != nil {
		out.Outcome = "uncertain"
		return out, videos.FailureCode("mutation_uncertain")
	}
	if !reflect.DeepEqual(observed.snippet, plan.after) {
		out.Outcome = "verification_failed"
		return out, videos.FailureCode("verification_failed")
	}
	out.Outcome = "verified"
	out.RequestedActionSatisfied = true
	return out, nil
}
