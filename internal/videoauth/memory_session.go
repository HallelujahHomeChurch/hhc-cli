package videoauth

import (
	"context"
	"sync"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

// MemorySession owns a grant only for this process. Construct it from a grant
// returned by a successful channel-verified Google flow, never an unverified
// user descriptor. It cannot restore a session or persist any credential.
type MemorySession struct{ state *memorySessionState }
type memorySessionState struct {
	mu               sync.Mutex
	grant            *GoogleCredential
	refreshing       chan struct{}
	refreshCancel    context.CancelFunc
	closed, invalid  bool
	done             chan struct{}
	channelID, scope string
	now              func() time.Time
}

func (MemorySession) String() string     { return "[redacted process-only Google session]" }
func (s MemorySession) GoString() string { return s.String() }

func NewMemorySession(verified GoogleCredential) (*MemorySession, error) {
	if !safeToken(verified.AccessToken) || !safeToken(verified.RefreshToken) || !safeToken(verified.ClientID) || verified.ClientSecret != "" && !safeToken(verified.ClientSecret) || !validGoogleScope(verified.Scope) || !youtube.ValidChannel(verified.ChannelID) || verified.ExpiresAt.IsZero() || verified.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return nil, videos.ErrInput
	}
	copy := verified
	return &MemorySession{state: &memorySessionState{grant: &copy, done: make(chan struct{}), channelID: verified.ChannelID, scope: verified.Scope, now: time.Now}}, nil
}

type SessionStatus struct {
	Authorized bool      `json:"authorized"`
	Refreshing bool      `json:"refreshing"`
	ChannelID  string    `json:"channelId,omitempty"`
	Scope      string    `json:"scope,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
}

func (SessionStatus) String() string     { return "[Google session metadata]" }
func (s SessionStatus) GoString() string { return s.String() }

func (s *MemorySession) Snapshot() SessionStatus {
	if s == nil || s.state == nil {
		return SessionStatus{}
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.invalid {
		return SessionStatus{}
	}
	status := SessionStatus{Refreshing: state.refreshing != nil, ChannelID: state.channelID, Scope: state.scope}
	if state.grant != nil {
		status.Authorized = state.grant.ExpiresAt.After(state.now())
		status.ExpiresAt = state.grant.ExpiresAt
	}
	return status
}

func shortCredential(g GoogleCredential) YouTubeCredential {
	return YouTubeCredential{AccessToken: g.AccessToken, Scope: g.Scope, ChannelID: g.ChannelID, ExpiresAt: g.ExpiresAt}
}

// Credential serializes refresh. Before the exchange the old grant is removed
// from usable state. Success replaces it only with the same client/channel/scope;
// any failure or uncertainty fences this session permanently until fresh consent.
// Process death loses the grant, so there is no persisted old state to replay.
func (s *MemorySession) Credential(ctx context.Context, refresh func(context.Context, GoogleCredential) (GoogleCredential, error)) (YouTubeCredential, error) {
	if s == nil || s.state == nil {
		return YouTubeCredential{}, videos.FailureCode("authentication_required")
	}
	state := s.state
	for {
		if err := ctx.Err(); err != nil {
			return YouTubeCredential{}, err
		}
		state.mu.Lock()
		if state.closed || state.invalid {
			state.mu.Unlock()
			return YouTubeCredential{}, videos.FailureCode("authentication_required")
		}
		if state.refreshing != nil {
			ready, done := state.refreshing, state.done
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return YouTubeCredential{}, ctx.Err()
			case <-done:
				return YouTubeCredential{}, videos.FailureCode("authentication_required")
			case <-ready:
				continue
			}
		}
		if state.grant == nil {
			state.invalid = true
			state.mu.Unlock()
			return YouTubeCredential{}, videos.FailureCode("authentication_required")
		}
		current := *state.grant
		if current.ExpiresAt.After(state.now().Add(time.Minute)) {
			state.mu.Unlock()
			return shortCredential(current), nil
		}
		if refresh == nil {
			state.grant = nil
			state.invalid = true
			state.mu.Unlock()
			return YouTubeCredential{}, videos.FailureCode("authentication_required")
		}
		state.grant = nil
		ready := make(chan struct{})
		state.refreshing = ready
		refreshCtx, cancel := context.WithCancel(ctx)
		state.refreshCancel = cancel
		state.mu.Unlock()
		next, err := refresh(refreshCtx, current)
		cancel()
		state.mu.Lock()
		state.refreshCancel = nil
		state.refreshing = nil
		valid := err == nil && next.ClientID == current.ClientID && next.ClientSecret == current.ClientSecret && next.ChannelID == current.ChannelID && next.Scope == current.Scope && safeToken(next.AccessToken) && safeToken(next.RefreshToken) && next.ExpiresAt.After(state.now().Add(time.Minute)) && !next.ExpiresAt.After(state.now().Add(24*time.Hour))
		if !state.closed && valid {
			copy := next
			state.grant = &copy
		} else {
			state.grant = nil
			state.invalid = true
		}
		close(ready)
		closed := state.closed
		state.mu.Unlock()
		if closed {
			return YouTubeCredential{}, videos.FailureCode("authentication_required")
		}
		if !valid {
			if ctx.Err() != nil {
				return YouTubeCredential{}, ctx.Err()
			}
			return YouTubeCredential{}, videos.FailureCode("session_refresh_uncertain")
		}
		if err = ctx.Err(); err != nil {
			return YouTubeCredential{}, err
		}
		return shortCredential(next), nil
	}
}

func (s *MemorySession) Close() {
	if s == nil || s.state == nil {
		return
	}
	state := s.state
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	state.closed = true
	state.grant = nil
	cancel := state.refreshCancel
	close(state.done)
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
