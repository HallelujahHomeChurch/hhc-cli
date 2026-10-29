// Package queryruntime binds authenticated callers to providers. It deliberately
// does not implement an OAuth grant or refresh-token store. A deployment must
// supply its reviewed identity/credential broker over a private Unix socket.
package queryruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/hhc"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type Binding struct {
	CallerID  string `json:"callerId"`
	BindingID string `json:"bindingId"`
	HHC       struct {
		Enabled bool   `json:"enabled"`
		Access  string `json:"access"`
	} `json:"hhc"`
	YouTube struct {
		Enabled   bool   `json:"enabled"`
		Access    string `json:"access"`
		ChannelID string `json:"channelId"`
	} `json:"youtube"`
}
type Broker struct {
	client      *http.Client
	newUpstream func(string) *queryhttp.Client
}

func NewBroker(socket string) (*Broker, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, videos.ErrInput
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}, MaxConnsPerHost: 32, MaxIdleConnsPerHost: 4, ResponseHeaderTimeout: 3 * time.Second}
	return &Broker{newUpstream: queryhttp.New, client: &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (b *Broker) request(ctx context.Context, path, bearer string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return videos.FailureCode("broker_unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://broker"+path, bytes.NewReader(data))
	if err != nil {
		return videos.FailureCode("broker_unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return videos.FailureCode("broker_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		code := "broker_unavailable"
		if resp.StatusCode == 401 {
			code = "authentication_required"
		}
		if resp.StatusCode == 403 {
			code = "permission_denied"
		}
		if resp.StatusCode == 429 {
			code = "rate_limited"
		}
		return &videos.Failure{Code: code, HTTPStatus: resp.StatusCode, Retryable: resp.StatusCode >= 500 || resp.StatusCode == 429}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return videos.FailureCode("broker_unavailable")
	}
	defer clear(raw)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		return videos.FailureCode("broker_unavailable")
	}
	return nil
}
func (b *Broker) Ready(ctx context.Context) error {
	var out struct {
		Ready bool `json:"ready"`
	}
	if err := b.request(ctx, "/v1/ready", "", struct{}{}, &out); err != nil {
		return err
	}
	if !out.Ready {
		return videos.FailureCode("broker_unavailable")
	}
	return nil
}
func (b *Broker) Authorize(ctx context.Context, bearer, tool string) (Binding, error) {
	var out Binding
	if len(bearer) < 16 || len(bearer) > 8192 || strings.ContainsAny(bearer, "\r\n\x00 ") {
		return out, videos.FailureCode("authentication_required")
	}
	if err := b.request(ctx, "/v1/authorize", bearer, struct {
		Audience string `json:"audience"`
		Tool     string `json:"tool"`
	}{"hhc-query", tool}, &out); err != nil {
		return out, err
	}
	if out.CallerID == "" || len(out.CallerID) > 256 || out.BindingID == "" || len(out.BindingID) > 256 || out.HHC.Enabled && out.HHC.Access != "public" && out.HHC.Access != "human" || out.YouTube.Enabled && (out.YouTube.Access != "public" && out.YouTube.Access != "owner" || !youtube.ValidChannel(out.YouTube.ChannelID)) {
		return Binding{}, videos.FailureCode("permission_denied")
	}
	return out, nil
}

type credential struct {
	CallerID  string    `json:"callerId"`
	BindingID string    `json:"bindingId"`
	Source    string    `json:"source"`
	Resource  string    `json:"resource"`
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (credential) String() string     { return "[redacted credential]" }
func (v credential) GoString() string { return v.String() }

type credentialRequest struct {
	CallerID  string `json:"callerId"`
	BindingID string `json:"bindingId"`
	Source    string `json:"source"`
	Resource  string `json:"resource"`
}

func (b *Broker) token(ctx context.Context, binding Binding, source, resource string) (credential, error) {
	var out credential
	if err := b.request(ctx, "/v1/credential", "", credentialRequest{binding.CallerID, binding.BindingID, source, resource}, &out); err != nil {
		return out, err
	}
	if out.CallerID != binding.CallerID || out.BindingID != binding.BindingID || out.Source != source || out.Resource != resource || out.Token == "" || len(out.Token) > 8192 || strings.ContainsAny(out.Token, "\r\n\x00 ") || time.Until(out.ExpiresAt) < 40*time.Second {
		return credential{}, videos.FailureCode("permission_denied")
	}
	return out, nil
}
func (b *Broker) permit(binding Binding, source, resource string) queryhttp.Permit {
	return func(ctx context.Context, bucket string, cost int) error {
		var out struct {
			Allowed bool `json:"allowed"`
		}
		in := struct {
			credentialRequest
			Bucket string `json:"bucket"`
			Cost   int    `json:"cost"`
		}{credentialRequest{binding.CallerID, binding.BindingID, source, resource}, bucket, cost}
		if err := b.request(ctx, "/v1/permit", "", in, &out); err != nil {
			return err
		}
		if !out.Allowed {
			return videos.FailureCode("permission_denied")
		}
		return nil
	}
}

// Query creates request-scoped providers and rechecks authorization after reads,
// discarding results on revocation. Tokens and private metadata are never cached.
func (b *Broker) Query(ctx context.Context, binding Binding, tool string, in videos.Input) (videos.Result, error) {
	service := videos.Service{}
	// Credential acquisition is lazy so one unavailable source cannot prevent
	// successful results from the other source.
	if binding.HHC.Enabled {
		service.HHC = lazy{build: func() (videos.Provider, error) {
			c := b.newUpstream(hhc.PublicOrigin)
			grant := hhc.Grant{}
			if binding.HHC.Access == "human" {
				v, err := b.token(ctx, binding, "hhc", "content:videos")
				if err != nil {
					return nil, err
				}
				if v.Kind != "human" {
					return nil, videos.FailureCode("permission_denied")
				}
				c = b.newUpstream(hhc.AdminOrigin)
				grant = hhc.Grant{Human: true, Scope: v.Scope, Bearer: v.Token}
			}
			c.Permit = b.permit(binding, "hhc", "content:videos")
			return &hhc.Provider{Client: c, PublicClient: b.newUpstream(hhc.PublicOrigin), Grant: grant}, nil
		}}
	}
	if binding.YouTube.Enabled {
		service.YouTube = lazy{build: func() (videos.Provider, error) {
			v, err := b.token(ctx, binding, "youtube", binding.YouTube.ChannelID)
			if err != nil {
				return nil, err
			}
			g := youtube.Grant{ChannelID: binding.YouTube.ChannelID}
			switch {
			case binding.YouTube.Access == "public" && v.Kind == "api_key":
				g.APIKey = v.Token
			case binding.YouTube.Access == "owner" && v.Kind == "oauth":
				g.Bearer = v.Token
				g.Scope = v.Scope
			default:
				return nil, videos.FailureCode("permission_denied")
			}
			c := b.newUpstream(youtube.Origin)
			c.Permit = b.permit(binding, "youtube", binding.YouTube.ChannelID)
			return &youtube.Provider{Client: c, Grant: g}, nil
		}}
	}
	result, err := service.Execute(ctx, tool, in)
	if err != nil {
		return result, err
	}
	// Only recheck selected sources; unrelated disabled grants must not suppress a
	// successful query. Any failed recheck clears that source's response.
	for i := range result.Sources {
		r := &result.Sources[i]
		if r.Error != nil {
			continue
		}
		var check queryhttp.Permit
		if r.Source == "hhc" && binding.HHC.Enabled {
			check = b.permit(binding, "hhc", "content:videos")
		}
		if r.Source == "youtube" && binding.YouTube.Enabled {
			check = b.permit(binding, "youtube", binding.YouTube.ChannelID)
		}
		if check != nil {
			if err := check(ctx, "authorization", 0); err != nil {
				r.Page = videos.Page{Items: []videos.Video{}}
				r.Error = videos.SafeFailure(err)
				result.Incomplete = true
				if r.Source == "hhc" {
					result.WebsiteStatus = "unknown"
				}
			}
		}
	}
	return result, nil
}

type lazy struct {
	build func() (videos.Provider, error)
}

func (l lazy) Search(ctx context.Context, in videos.Input) (videos.Page, error) {
	p, e := l.build()
	if e != nil {
		return videos.Page{}, e
	}
	return p.Search(ctx, in)
}
func (l lazy) Get(ctx context.Context, id string, in videos.Input) (videos.Page, error) {
	p, e := l.build()
	if e != nil {
		return videos.Page{}, e
	}
	return p.Get(ctx, id, in)
}
