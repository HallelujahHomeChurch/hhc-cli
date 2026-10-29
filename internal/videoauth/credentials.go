// Package videoauth accepts explicitly injected, short-lived query credentials.
// It does not discover, persist, authorize or refresh credentials implicitly.
package videoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type YouTubeCredential struct {
	AccessToken string    `json:"accessToken"`
	Scope       string    `json:"scope"`
	ChannelID   string    `json:"channelId"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type HHCCredential struct {
	AccessToken   string    `json:"accessToken"`
	Scope         string    `json:"scope"`
	PrincipalType string    `json:"principalType"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type Credentials struct {
	YouTube *YouTubeCredential `json:"youtube,omitempty"`
	HHC     *HHCCredential     `json:"hhc,omitempty"`
}

func (Credentials) String() string           { return "[redacted query credentials]" }
func (c Credentials) GoString() string       { return c.String() }
func (YouTubeCredential) String() string     { return "[redacted YouTube credential]" }
func (c YouTubeCredential) GoString() string { return c.String() }
func (HHCCredential) String() string         { return "[redacted HHC credential]" }
func (c HHCCredential) GoString() string     { return c.String() }

func safeToken(s string) bool {
	if len(s) == 0 || len(s) > 8192 {
		return false
	}
	for _, b := range []byte(s) {
		if b <= 32 || b >= 127 {
			return false
		}
	}
	return true
}

func expiry(at, now time.Time) error {
	if at.IsZero() || at.After(now.Add(24*time.Hour)) {
		return videos.ErrInput
	}
	if !at.After(now) {
		return videos.FailureCode("authentication_required")
	}
	return nil
}

// Validate checks only the injected descriptor. Upstream servers remain the
// authority on actual ownership, scope, audience and revocation.
func (c Credentials) Validate(now time.Time) error {
	if c.YouTube == nil && c.HHC == nil {
		return videos.ErrInput
	}
	if c.YouTube != nil {
		v := c.YouTube
		if !safeToken(v.AccessToken) || !youtube.ValidChannel(v.ChannelID) {
			return videos.ErrInput
		}
		if v.Scope != youtube.ReadScope {
			return videos.FailureCode("permission_denied")
		}
		if err := expiry(v.ExpiresAt, now); err != nil {
			return err
		}
	}
	if c.HHC != nil {
		v := c.HHC
		if !safeToken(v.AccessToken) {
			return videos.ErrInput
		}
		if v.PrincipalType != "human" || !hhcReadScope(v.Scope) {
			return videos.FailureCode("permission_denied")
		}
		if err := expiry(v.ExpiresAt, now); err != nil {
			return err
		}
	}
	return nil
}

func hhcReadScope(s string) bool {
	read, offline := false, false
	for _, field := range strings.Fields(s) {
		switch field {
		case "cms:pages:read":
			if read {
				return false
			}
			read = true
		case "offline_access":
			if offline {
				return false
			}
			offline = true
		default:
			return false
		}
	}
	return read
}

// CheckYouTube and CheckHHC can be used as queryhttp.Permit callbacks. They check
// expiry again before every upstream attempt, without retaining shared state.
func (c Credentials) CheckYouTube(ctx context.Context, _ string, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.YouTube == nil {
		return videos.FailureCode("authentication_required")
	}
	return (Credentials{YouTube: c.YouTube}).Validate(time.Now())
}
func (c Credentials) CheckHHC(ctx context.Context, _ string, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.HHC == nil {
		return videos.FailureCode("authentication_required")
	}
	return (Credentials{HHC: c.HHC}).Validate(time.Now())
}

// Read consumes one bounded JSON object from an actual pipe. On cancellation it
// closes input to release a blocked read; the caller must own that pipe endpoint.
// Regular files, terminals and devices are rejected. No credential is saved.
func Read(ctx context.Context, input *os.File) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	if input == nil {
		return Credentials{}, videos.ErrInput
	}
	info, err := input.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return Credentials{}, videos.ErrInput
	}
	stop := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stop()
	raw, err := io.ReadAll(io.LimitReader(input, 16385))
	defer clear(raw)
	if ctx.Err() != nil {
		return Credentials{}, ctx.Err()
	}
	if err != nil || len(raw) > 16384 || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		return Credentials{}, videos.ErrInput
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return Credentials{}, videos.ErrInput
	}
	for key, value := range fields {
		if key != "youtube" && key != "hhc" || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Credentials{}, videos.ErrInput
		}
	}
	var c Credentials
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Credentials{}, videos.ErrInput
	}
	if err = c.Validate(time.Now()); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// Refuse duplicate keys as well as invalid/trailing JSON rather than allowing
// one parser or secret injector to interpret a different credential descriptor.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var read func(int) error
	read = func(depth int) error {
		if depth > 12 {
			return videos.ErrInput
		}
		token, err := d.Token()
		if err != nil {
			return videos.ErrInput
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delim != '{' && delim != '[' {
			return videos.ErrInput
		}
		keys := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, e := d.Token()
				if e != nil {
					return videos.ErrInput
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return videos.ErrInput
				}
				keys[name] = true
			}
			if err := read(depth + 1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
			return videos.ErrInput
		}
		return nil
	}
	if err := read(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return videos.ErrInput
	}
	return nil
}
