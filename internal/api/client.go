package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
)

const origin = "https://admin.alive.org.tw"

var ErrInvalidResponse = errors.New("invalid_api_response")
var recordID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type Error struct {
	Code       string
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }

type Recording struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

type Client struct {
	http  *http.Client
	token auth.Token
	renew func(context.Context) (auth.Token, error)
	mu    sync.Mutex
}

func NewClient(token auth.Token, renew func(context.Context) (auth.Token, error)) *Client {
	return &Client{token: token, renew: renew, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func ValidRecordingID(id string) bool {
	return recordID.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func (c *Client) Principal() auth.Principal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.Principal()
}

func (c *Client) GetRecording(ctx context.Context, id string) (Recording, error) {
	if !ValidRecordingID(id) {
		return Recording{}, auth.ErrInvalidAuthInput
	}
	var value Recording
	err := c.request(ctx, http.MethodGet, "/api/admin/recordings/"+id, "cms:recordings:read", nil, nil, &value)
	if err != nil {
		return Recording{}, err
	}
	if value.ID != id || value.Version < 1 || !slices.Contains([]string{"draft", "published"}, value.Status) {
		return Recording{}, ErrInvalidResponse
	}
	return value, nil
}

func (c *Client) refresh(ctx context.Context) error {
	if c.renew == nil {
		return auth.ErrAuthenticationRequired
	}
	next, err := c.renew(ctx)
	if err != nil {
		return err
	}
	before, after := c.token.Principal(), next.Principal()
	if before.ID == "" || before.ID != after.ID || before.Type != after.Type || before.ClientID != after.ClientID || next.Bearer() == "" || !time.Now().Before(next.ExpiresAt()) {
		return auth.ErrAuthenticationRequired
	}
	c.token = next
	return nil
}

// Control calls are serialized; presigned byte uploads use a separate client.
// Mutations never auto-retry on network/5xx: the operation owner reconciles
// server state using its durable key before deciding the next action.
func (c *Client) request(ctx context.Context, method, path, scope string, body []byte, headers http.Header, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.token.Bearer() == "" {
		return auth.ErrAuthenticationRequired
	}
	if !time.Now().Before(c.token.ExpiresAt()) {
		if err := c.refresh(ctx); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !slices.Contains(strings.Fields(c.token.Scope()), scope) {
			return auth.ErrPermissionDenied
		}
		req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
		if err != nil {
			return auth.ErrInvalidAuthInput
		}
		req.Header = headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Authorization", "Bearer "+c.token.Bearer())
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Code: "api_unavailable"}
		}
		if response.StatusCode == 401 {
			response.Body.Close()
			if attempt == 1 {
				return auth.ErrAuthenticationRequired
			}
			if err := c.refresh(ctx); err != nil {
				return err
			}
			continue
		}
		err = decodeResponse(response, result)
		response.Body.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return auth.ErrAuthenticationRequired
}

func decodeResponse(response *http.Response, result any) error {
	switch response.StatusCode {
	case 200:
	case 403:
		return auth.ErrPermissionDenied
	case 404:
		return &Error{Code: "not_found", Status: 404}
	case 409:
		return &Error{Code: "operation_conflict", Status: 409}
	case 412, 428:
		return &Error{Code: "state_changed", Status: response.StatusCode}
	case 429:
		return &Error{Code: "rate_limited", Status: 429, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	default:
		if response.StatusCode >= 500 {
			return &Error{Code: "api_unavailable", Status: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
		}
		if response.StatusCode == 400 || response.StatusCode == 413 {
			return auth.ErrInvalidAuthInput
		}
		return ErrInvalidResponse
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return ErrInvalidResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !utf8.Valid(data) {
		return ErrInvalidResponse
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) || len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return ErrInvalidResponse
	}
	if json.Unmarshal(envelope.Data, result) != nil {
		return ErrInvalidResponse
	}
	return nil
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, min(time.Until(at), 24*time.Hour))
	}
	return time.Second
}
