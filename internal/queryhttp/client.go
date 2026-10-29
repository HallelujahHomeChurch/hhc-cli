// Package queryhttp is the bounded, read-only upstream transport. It never
// returns raw response bodies, URLs or transport errors to an adapter.
package queryhttp

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

type Permit func(context.Context, string, int) error

// Client is composed by the CLI; query arguments cannot choose destinations.
type Client struct {
	HTTP   *http.Client
	Base   string
	Permit Permit
}

func New(base string) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Get(ctx context.Context, path string, q url.Values, h http.Header, bucket string, cost int, out any) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return videos.FailureCode("invalid_response")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.Permit != nil {
			if err := c.Permit(ctx, bucket, cost); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path+"?"+q.Encode(), nil)
		if err != nil {
			return videos.FailureCode("unavailable")
		}
		req.Header = h.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return videos.FailureCode("unavailable")
		}
		status := resp.StatusCode
		if (status == 429 || status == 502 || status == 503 || status == 504) && attempt == 0 {
			delay := 200 * time.Millisecond
			if v := resp.Header.Get("Retry-After"); v != "" {
				if n, e := strconv.Atoi(v); e == nil && n >= 0 && n <= 2 {
					delay = time.Duration(n) * time.Second
				} else if at, e := http.ParseTime(v); e == nil && time.Until(at) <= 2*time.Second {
					delay = max(0, time.Until(at))
				} else {
					resp.Body.Close()
					return failure(status)
				}
			}
			resp.Body.Close()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if status != 200 {
			resp.Body.Close()
			return failure(status)
		}
		// Reject explicitly stale upstream metadata instead of asserting historical
		// visibility or publication status. No application cache stores private data.
		age, _ := strconv.Atoi(resp.Header.Get("Age"))
		if age > 30 || strings.Contains(resp.Header.Get("Warning"), "110") {
			resp.Body.Close()
			return videos.FailureCode("stale_response")
		}
		mt, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if e != nil || mt != "application/json" {
			resp.Body.Close()
			return videos.FailureCode("invalid_response")
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		resp.Body.Close()
		if e != nil || len(data) > 2<<20 || !utf8.Valid(data) || json.Unmarshal(data, out) != nil {
			return videos.FailureCode("invalid_response")
		}
		return nil
	}
	return videos.FailureCode("unavailable")
}
func failure(status int) error {
	code := "unavailable"
	retry := status >= 500 || status == 429
	switch status {
	case 401:
		code = "authentication_required"
	case 403:
		code = "permission_or_quota_denied"
	case 404:
		code = "not_found"
	case 429:
		code = "rate_limited"
	}
	return &videos.Failure{Code: code, HTTPStatus: status, Retryable: retry}
}
