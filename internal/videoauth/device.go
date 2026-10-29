package videoauth

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

const googleDeviceURL = "https://oauth2.googleapis.com/device/code"
const googleManageScope = "https://www.googleapis.com/auth/youtube"

// DeviceConfig identifies an existing Google TVs and Limited Input OAuth client.
// This code does not register a client or pretend to be a different device type.
// Google allows readonly or youtube for device authorization, not force-ssl.
type DeviceConfig struct {
	ClientID     string
	ClientSecret string
	ChannelID    string
	Access       string // read or edit; edit explicitly requests the broader youtube scope
}

func (DeviceConfig) String() string     { return "[redacted Google device configuration]" }
func (c DeviceConfig) GoString() string { return c.String() }

type GoogleDevice struct {
	config                           DeviceConfig
	scope                            string
	http                             *http.Client
	deviceURL, tokenURL, channelsURL string
	now                              func() time.Time
	wait                             func(context.Context, time.Duration) error
}

func (GoogleDevice) String() string     { return "[redacted Google device client]" }
func (d GoogleDevice) GoString() string { return d.String() }

func NewGoogleDevice(config DeviceConfig) (*GoogleDevice, error) {
	if !safeToken(config.ClientID) || len(config.ClientID) > 512 || !safeToken(config.ClientSecret) || !youtube.ValidChannel(config.ChannelID) {
		return nil, videos.ErrInput
	}
	scope := youtube.ReadScope
	switch config.Access {
	case "read":
	case "edit":
		scope = googleManageScope
	default:
		return nil, videos.ErrInput
	}
	return &GoogleDevice{config: config, scope: scope, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, deviceURL: googleDeviceURL, tokenURL: googleTokenURL, channelsURL: googleChannelsURL, now: time.Now, wait: deviceWait}, nil
}

func deviceWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DevicePresentation contains only values intended for the consenting user's UI.
// Preserve the URL/code exactly; display fields explicitly, never as shell code.
type DevicePresentation struct {
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func (DevicePresentation) String() string     { return "[Google device authorization presentation]" }
func (p DevicePresentation) GoString() string { return p.String() }

type DeviceSession struct{ data *deviceSessionData }
type deviceSessionData struct {
	mu           sync.Mutex
	owner        *GoogleDevice
	deviceCode   string
	presentation DevicePresentation
	interval     time.Duration
	used         bool
}

func (DeviceSession) String() string     { return "[redacted Google device session]" }
func (s DeviceSession) GoString() string { return s.String() }

func (s *DeviceSession) Presentation() DevicePresentation {
	if s == nil || s.data == nil {
		return DevicePresentation{}
	}
	d := s.data
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.used || !d.presentation.ExpiresAt.After(d.owner.now()) {
		return DevicePresentation{}
	}
	return d.presentation
}

func printable(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, b := range []byte(s) {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}

func verificationURL(raw string) bool {
	if !printable(raw, 2048) || strings.Contains(raw, " ") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Port() != "" {
		return false
	}
	switch u.Host {
	case "google.com", "www.google.com", "accounts.google.com", "g.co":
	default:
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for _, key := range []string{"device_code", "access_token", "refresh_token", "client_secret"} {
		if q.Has(key) {
			return false
		}
	}
	return true
}

func googleJSON(resp *http.Response) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		clear(raw)
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	return raw, nil
}

func (d *GoogleDevice) Begin(ctx context.Context) (*DeviceSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := d.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.deviceURL, strings.NewReader(url.Values{"client_id": {d.config.ClientID}, "scope": {d.scope}}.Encode()))
	if err != nil {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	raw, err := googleJSON(resp)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var v struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		VerificationURI string `json:"verification_uri"`
		Expires         int    `json:"expires_in"`
		Interval        *int   `json:"interval"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	if v.VerificationURL == "" {
		v.VerificationURL = v.VerificationURI
	} else if v.VerificationURI != "" && v.VerificationURI != v.VerificationURL {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	interval := 5
	if v.Interval != nil {
		interval = *v.Interval
	}
	if !safeToken(v.DeviceCode) || !printable(v.UserCode, 64) || !verificationURL(v.VerificationURL) || v.Expires < 1 || v.Expires > 3600 || interval < 1 || interval > 60 {
		return nil, videos.FailureCode("device_authorization_unavailable")
	}
	expires := started.Add(time.Duration(v.Expires) * time.Second)
	if !expires.After(d.now()) {
		return nil, videos.FailureCode("device_authorization_expired")
	}
	return &DeviceSession{data: &deviceSessionData{owner: d, deviceCode: v.DeviceCode, interval: time.Duration(interval) * time.Second, presentation: DevicePresentation{VerificationURL: v.VerificationURL, UserCode: v.UserCode, ExpiresAt: expires}}}, nil
}

// Poll claims the session once for its whole bounded polling lifetime. Only
// explicit pending/slow_down responses allow another request. Network/HTTP/
// malformed-success uncertainty consumes the session and never replays its code.
func (d *GoogleDevice) Poll(ctx context.Context, s *DeviceSession) (GoogleCredential, error) {
	if err := ctx.Err(); err != nil {
		return GoogleCredential{}, err
	}
	if s == nil || s.data == nil {
		return GoogleCredential{}, videos.ErrInput
	}
	state := s.data
	state.mu.Lock()
	if state.owner != d || state.used {
		state.mu.Unlock()
		return GoogleCredential{}, videos.FailureCode("device_session_used")
	}
	state.used = true
	code, interval, expires := state.deviceCode, state.interval, state.presentation.ExpiresAt
	state.deviceCode = ""
	state.mu.Unlock()
	remaining := expires.Sub(d.now())
	if remaining <= 0 {
		return GoogleCredential{}, videos.FailureCode("device_authorization_expired")
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	for attempt := 0; attempt < 3600; attempt++ {
		remaining = expires.Sub(d.now())
		if remaining <= 0 || interval >= remaining {
			return GoogleCredential{}, videos.FailureCode("device_authorization_expired")
		}
		next := d.now().Add(interval)
		if err := d.wait(ctx, interval); err != nil {
			return GoogleCredential{}, err
		}
		if d.now().Before(next) {
			return GoogleCredential{}, videos.FailureCode("device_poll_timing")
		}
		if !expires.After(d.now()) {
			return GoogleCredential{}, videos.FailureCode("device_authorization_expired")
		}
		if err := ctx.Err(); err != nil {
			return GoogleCredential{}, err
		}
		form := url.Values{"client_id": {d.config.ClientID}, "client_secret": {d.config.ClientSecret}, "device_code": {code}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.tokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := d.http.Do(req)
		if err != nil {
			return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
		}
		if resp.StatusCode == http.StatusOK {
			current := GoogleCredential{ClientID: d.config.ClientID, ClientSecret: d.config.ClientSecret, Scope: d.scope, ChannelID: d.config.ChannelID}
			grant, err := googleTokenResponse(resp, current, "device_authorization_uncertain", true, true)
			resp.Body.Close()
			if err != nil {
				return GoogleCredential{}, err
			}
			verifier := &GoogleOAuth{config: GoogleOAuthConfig{ChannelID: d.config.ChannelID}, http: d.http, channelsURL: d.channelsURL}
			if err = verifier.verifyChannel(ctx, grant); err != nil {
				return GoogleCredential{}, err
			}
			return grant, nil
		}
		status := resp.StatusCode
		raw, err := googleJSON(resp)
		resp.Body.Close()
		if err != nil {
			return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
		}
		var failure struct {
			Error   string          `json:"error"`
			Access  json.RawMessage `json:"access_token"`
			Refresh json.RawMessage `json:"refresh_token"`
		}
		err = json.Unmarshal(raw, &failure)
		clear(raw)
		if err != nil || len(failure.Access) > 0 || len(failure.Refresh) > 0 {
			return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
		}
		switch failure.Error {
		case "authorization_pending":
			if status != 400 && status != 428 {
				return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
			}
		case "slow_down":
			if status != 400 && status != 403 {
				return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
			}
			interval += 5 * time.Second
		case "access_denied":
			return GoogleCredential{}, videos.FailureCode("device_authorization_denied")
		case "expired_token":
			return GoogleCredential{}, videos.FailureCode("device_authorization_expired")
		case "invalid_client", "unauthorized_client":
			return GoogleCredential{}, videos.FailureCode("authentication_required")
		default:
			return GoogleCredential{}, videos.FailureCode("device_authorization_uncertain")
		}
	}
	return GoogleCredential{}, videos.FailureCode("device_authorization_expired")
}
