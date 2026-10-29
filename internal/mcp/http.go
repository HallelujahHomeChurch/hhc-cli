package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryruntime"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

type Identity interface {
	Authorize(context.Context, string, string) (queryruntime.Binding, error)
	Query(context.Context, queryruntime.Binding, string, videos.Input) (videos.Result, error)
	Ready(context.Context) error
}
type Audit struct {
	CallerHash string `json:"callerHash"`
	Operation  string `json:"operation"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"durationMs"`
}

// OAuthConfig is trusted deployment configuration. URLs and scopes are never
// derived from Host, forwarded headers or tool arguments.
type OAuthConfig struct {
	ResourceURL          string
	AuthorizationServers []string
	ScopesSupported      []string
}
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}
type Server struct {
	Identity       Identity
	Hosts          []string
	Origins        []string
	Version        string
	Audit          func(Audit)
	draining       atomic.Bool
	active         chan struct{}
	oauthMetadata  *protectedResourceMetadata
	oauthChallenge string
}

func NewServer(identity Identity, hosts, origins []string, version string) *Server {
	return &Server{Identity: identity, Hosts: hosts, Origins: origins, Version: version, active: make(chan struct{}, 8)}
}

// NewServerWithOAuth exposes protected-resource discovery for remote MCP OAuth.
// The authorization server still owns client registration, consent and tokens.
func NewServerWithOAuth(identity Identity, hosts, origins []string, version string, config OAuthConfig) (*Server, error) {
	resource, err := safeOAuthURL(config.ResourceURL)
	if err != nil || resource.Path != "/mcp" || !has(hosts, resource.Host) || len(config.AuthorizationServers) < 1 || len(config.AuthorizationServers) > 8 || len(config.ScopesSupported) < 1 || len(config.ScopesSupported) > 16 {
		return nil, videos.ErrInput
	}
	for _, issuer := range config.AuthorizationServers {
		if _, err := safeOAuthURL(issuer); err != nil {
			return nil, videos.ErrInput
		}
	}
	seen := map[string]bool{}
	for _, scope := range config.ScopesSupported {
		if scope == "" || len(scope) > 128 || seen[scope] {
			return nil, videos.ErrInput
		}
		seen[scope] = true
		for _, b := range []byte(scope) {
			if b < 0x21 || b > 0x7e || b == '"' || b == '\\' {
				return nil, videos.ErrInput
			}
		}
	}
	metadataURL := *resource
	metadataURL.Path = "/.well-known/oauth-protected-resource/mcp"
	s := NewServer(identity, hosts, origins, version)
	s.oauthMetadata = &protectedResourceMetadata{Resource: resource.String(), AuthorizationServers: append([]string(nil), config.AuthorizationServers...), ScopesSupported: append([]string(nil), config.ScopesSupported...), BearerMethodsSupported: []string{"header"}}
	s.oauthChallenge = `Bearer realm="hhc-query", resource_metadata="` + metadataURL.String() + `", scope="` + strings.Join(config.ScopesSupported, " ") + `"`
	return s, nil
}
func safeOAuthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, videos.ErrInput
	}
	return u, nil
}
func (s *Server) challenge(w http.ResponseWriter, code string) {
	challenge := s.oauthChallenge
	if challenge == "" {
		challenge = `Bearer realm="hhc-query"`
	}
	if code != "" {
		challenge += `, error="` + code + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
}

func (s *Server) Drain() { s.draining.Store(true) }
func has(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !has(s.Hosts, r.Host) {
		w.WriteHeader(403)
		return
	}
	if origin := r.Header.Get("Origin"); len(r.Header.Values("Origin")) > 1 || origin != "" && !has(s.Origins, origin) {
		w.WriteHeader(403)
		return
	}
	if s.oauthMetadata != nil && (r.URL.Path == "/.well-known/oauth-protected-resource/mcp" || r.URL.Path == "/.well-known/oauth-protected-resource") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(s.oauthMetadata)
		}
		return
	}
	if r.URL.Path == "/health/live" && r.Method == http.MethodGet {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path == "/health/ready" && r.Method == http.MethodGet {
		if s.draining.Load() || s.Identity == nil {
			w.WriteHeader(503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.Identity.Ready(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		return
	}
	if r.URL.Path != "/mcp" {
		w.WriteHeader(404)
		return
	}
	if s.draining.Load() || s.Identity == nil {
		w.WriteHeader(503)
		return
	}
	// GET streams and stateful sessions are intentionally unsupported in this
	// stateless transport. POST replies use application/json, no SSE/session cache.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	if !accepts(r.Header.Get("Accept"), "application/json") || !accepts(r.Header.Get("Accept"), "text/event-stream") {
		w.WriteHeader(406)
		return
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		w.WriteHeader(415)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && v != ProtocolVersion && v != "2025-06-18" {
		w.WriteHeader(400)
		return
	}
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		w.WriteHeader(413)
		return
	}
	req, err := Decode(body)
	if err != nil {
		w.WriteHeader(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	scheme, bearer, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || bearer == "" || len(r.Header.Values("Authorization")) != 1 {
		s.challenge(w, "")
		w.WriteHeader(401)
		return
	}
	binding, err := s.Identity.Authorize(ctx, bearer, Operation(req))
	if err != nil {
		f := videos.SafeFailure(err)
		status := 503
		if f.Code == "authentication_required" {
			status = 401
			s.challenge(w, "invalid_token")
		}
		if f.Code == "permission_denied" {
			status = 403
		}
		if f.Code == "insufficient_scope" {
			status = 403
			s.challenge(w, "insufficient_scope")
		}
		if f.Code == "rate_limited" {
			status = 429
		}
		w.WriteHeader(status)
		return
	}
	outcome := "ok"
	defer func() {
		if s.Audit != nil {
			hash := sha256.Sum256([]byte(binding.CallerID))
			s.Audit(Audit{CallerHash: hex.EncodeToString(hash[:16]), Operation: Operation(req), Outcome: outcome, DurationMS: time.Since(start).Milliseconds()})
		}
	}()
	if len(req.ID) == 0 {
		if req.Method != "notifications/initialized" && req.Method != "notifications/cancelled" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(202)
		return
	}
	var callerFailure *videos.Failure
	response := Handle(ctx, req, func(ctx context.Context, tool string, in videos.Input) (videos.Result, error) {
		v, e := s.Identity.Query(ctx, binding, tool, in)
		if e != nil {
			safe := videos.SafeFailure(e)
			outcome = safe.Code
			if safe.CallerAuthority {
				callerFailure = safe
			}
		}
		if v.Incomplete {
			outcome = "incomplete"
		}
		return v, e
	}, s.Version)
	if callerFailure != nil {
		status := 503
		switch callerFailure.Code {
		case "authentication_required":
			status = 401
			s.challenge(w, "invalid_token")
		case "permission_denied":
			status = 403
		case "insufficient_scope":
			status = 403
			s.challenge(w, "insufficient_scope")
		}
		w.WriteHeader(status)
		return
	}
	if response.Error != nil {
		outcome = "protocol_error"
	}
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(response); err != nil && !errors.Is(err, context.Canceled) {
		outcome = "response_failed"
	}
}

func accepts(header, expected string) bool {
	for _, part := range strings.Split(header, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || media != expected {
			continue
		}
		if q, ok := params["q"]; ok {
			weight, err := strconv.ParseFloat(q, 64)
			if err != nil || weight <= 0 || weight > 1 {
				continue
			}
		}
		return true
	}
	return false
}
