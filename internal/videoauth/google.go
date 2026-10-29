package videoauth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

// GoogleCredential belongs to an explicit OAuth/storage integration, never the
// query command's stdin contract. It is not loaded from disk or environment.
type GoogleCredential struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	AccessToken  string
	Scope        string
	ExpiresAt    time.Time
	ChannelID    string
}

func (GoogleCredential) String() string     { return "[redacted Google credential]" }
func (v GoogleCredential) GoString() string { return v.String() }

// GoogleRefresh exchanges one already-authorized exact read or edit refresh token.
// It does not create consent, coordinate concurrent processes or persist tokens.
// Before calling, an approved credential owner must durably fence and serialize
// the exchange; before using its result it must securely commit rotated tokens.
// After an ambiguous result the owner must not replay the previous refresh token.
// The CLI intentionally does not invoke this adapter implicitly.
type GoogleRefresh struct {
	http     *http.Client
	endpoint string
}

func NewGoogleRefresh() *GoogleRefresh {
	return &GoogleRefresh{
		http:     &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		endpoint: "https://oauth2.googleapis.com/token",
	}
}

func (r *GoogleRefresh) Refresh(ctx context.Context, current GoogleCredential) (GoogleCredential, error) {
	if err := ctx.Err(); err != nil {
		return GoogleCredential{}, err
	}
	if !validGoogleScope(current.Scope) || !safeToken(current.ClientID) || !safeToken(current.RefreshToken) || current.ClientSecret != "" && !safeToken(current.ClientSecret) {
		return GoogleCredential{}, videos.FailureCode("permission_denied")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {current.RefreshToken}, "client_id": {current.ClientID}, "scope": {current.Scope}}
	if current.ClientSecret != "" {
		form.Set("client_secret", current.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return GoogleCredential{}, videos.FailureCode("refresh_uncertain")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return GoogleCredential{}, videos.FailureCode("refresh_uncertain")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			return GoogleCredential{}, videos.FailureCode("authentication_required")
		}
		return GoogleCredential{}, videos.FailureCode("refresh_uncertain")
	}
	return googleTokenResponse(resp, current, "refresh_uncertain", false)
}

func validGoogleScope(scope string) bool {
	return scope == youtube.ReadScope || scope == "https://www.googleapis.com/auth/youtube.force-ssl"
}

// Parse only Google's bounded token response; scope omission means the exact
// requested scope remains unchanged, never an inferred permission increase.
func googleTokenResponse(resp *http.Response, current GoogleCredential, uncertain string, requireRefresh bool) (GoogleCredential, error) {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return GoogleCredential{}, videos.FailureCode(uncertain)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	defer clear(raw)
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		return GoogleCredential{}, videos.FailureCode(uncertain)
	}
	var v struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Scope   string `json:"scope"`
		Expires int64  `json:"expires_in"`
		Type    string `json:"token_type"`
	}
	if json.Unmarshal(raw, &v) != nil || !strings.EqualFold(v.Type, "Bearer") || !safeToken(v.Access) || v.Expires < 61 || v.Expires > 86400 || v.Refresh != "" && !safeToken(v.Refresh) || requireRefresh && v.Refresh == "" || v.Scope != "" && v.Scope != current.Scope {
		return GoogleCredential{}, videos.FailureCode(uncertain)
	}
	// RFC6749 permits omitting scope when unchanged. Never infer a broader scope.
	next := current
	next.AccessToken, next.ExpiresAt = v.Access, time.Now().Add(time.Duration(v.Expires)*time.Second)
	if v.Refresh != "" {
		next.RefreshToken = v.Refresh
	}
	return next, nil
}
