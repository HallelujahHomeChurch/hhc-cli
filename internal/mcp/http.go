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
type Server struct {
	Identity Identity
	Hosts    []string
	Origins  []string
	Version  string
	Audit    func(Audit)
	draining atomic.Bool
	active   chan struct{}
}

func NewServer(identity Identity, hosts, origins []string, version string) *Server {
	return &Server{Identity: identity, Hosts: hosts, Origins: origins, Version: version, active: make(chan struct{}, 8)}
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
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(r.Header.Values("Authorization")) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hhc-query"`)
		w.WriteHeader(401)
		return
	}
	binding, err := s.Identity.Authorize(ctx, bearer, Operation(req))
	if err != nil {
		f := videos.SafeFailure(err)
		status := 503
		if f.Code == "authentication_required" {
			status = 401
			w.Header().Set("WWW-Authenticate", `Bearer realm="hhc-query"`)
		}
		if f.Code == "permission_denied" {
			status = 403
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
	response := Handle(ctx, req, func(ctx context.Context, tool string, in videos.Input) (videos.Result, error) {
		v, e := s.Identity.Query(ctx, binding, tool, in)
		if e != nil {
			outcome = videos.SafeFailure(e).Code
		}
		if v.Incomplete {
			outcome = "incomplete"
		}
		return v, e
	}, s.Version)
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
