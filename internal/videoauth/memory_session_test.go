package videoauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

func memoryGrant() GoogleCredential {
	return GoogleCredential{ClientID: "fixture-client", ClientSecret: "client-SECRET", AccessToken: "access-SECRET", RefreshToken: "refresh-SECRET", Scope: youtube.ReadScope, ChannelID: oauthFixtureChannel, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestMemorySessionShortCredentialAndClose(t *testing.T) {
	s, err := NewMemorySession(memoryGrant())
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.Credential(context.Background(), nil)
	if err != nil || cred.AccessToken != "access-SECRET" || cred.ChannelID != oauthFixtureChannel {
		t.Fatal("valid session failed", err)
	}
	status := s.Snapshot()
	if !status.Authorized || status.Refreshing || status.ChannelID != oauthFixtureChannel {
		t.Fatal("invalid metadata")
	}
	for _, v := range []any{s, *s, status} {
		if strings.Contains(fmt.Sprintf("%v %#v", v, v), "SECRET") {
			t.Fatal("session secret formatted")
		}
	}
	s.Close()
	s.Close()
	if s.Snapshot().Authorized {
		t.Fatal("closed session authorized")
	}
	if _, err = s.Credential(context.Background(), nil); err == nil {
		t.Fatal("closed credential returned")
	}
	if _, err = new(MemorySession).Credential(context.Background(), nil); err == nil {
		t.Fatal("zero session authorized")
	}
}

func TestMemoryRefreshSerializedAndRotated(t *testing.T) {
	g := memoryGrant()
	g.ExpiresAt = time.Now().Add(10 * time.Second)
	s, err := NewMemorySession(g)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	refresh := func(ctx context.Context, current GoogleCredential) (GoogleCredential, error) {
		if calls.Add(1) != 1 {
			t.Error("concurrent old token refresh")
		}
		close(entered)
		<-release
		if current.RefreshToken != "refresh-SECRET" {
			t.Error("wrong refresh token")
		}
		current.RefreshToken = "rotated-SECRET"
		current.AccessToken = "new-access-SECRET"
		current.ExpiresAt = time.Now().Add(time.Hour)
		return current, nil
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred, err := s.Credential(context.Background(), refresh)
			if err == nil && cred.AccessToken == "new-access-SECRET" {
				successes.Add(1)
			}
		}()
	}
	<-entered
	if !s.Snapshot().Refreshing || s.Snapshot().Authorized {
		t.Fatal("old credential usable during refresh")
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 12 || !s.Snapshot().Authorized {
		t.Fatal("single-flight failure", calls.Load(), successes.Load())
	}
	if s.state.grant.RefreshToken != "rotated-SECRET" {
		t.Fatal("rotated token not retained")
	}
}

func TestMemoryRefreshFailureFencesWithoutReplay(t *testing.T) {
	for _, mode := range []string{"uncertain", "scope", "channel", "client", "secret", "expiry", "token"} {
		t.Run(mode, func(t *testing.T) {
			g := memoryGrant()
			g.ExpiresAt = time.Now().Add(time.Second)
			s, _ := NewMemorySession(g)
			calls := 0
			refresh := func(ctx context.Context, current GoogleCredential) (GoogleCredential, error) {
				calls++
				current.ExpiresAt = time.Now().Add(time.Hour)
				switch mode {
				case "uncertain":
					return GoogleCredential{}, errors.New("SECRET failure")
				case "scope":
					current.Scope = googleManageScope
				case "channel":
					current.ChannelID = "UC1234567890123456789012"
				case "client":
					current.ClientID = "different"
				case "secret":
					current.ClientSecret = "different"
				case "expiry":
					current.ExpiresAt = time.Now()
				case "token":
					current.AccessToken = ""
				}
				return current, nil
			}
			if cred, err := s.Credential(context.Background(), refresh); err == nil || cred.AccessToken != "" || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe refresh result")
			}
			if _, err := s.Credential(context.Background(), refresh); err == nil || calls != 1 || s.Snapshot().Authorized || s.state.grant != nil {
				t.Fatal("failed old grant replayed", err, calls)
			}
		})
	}
}

func TestMemoryCloseCancelsRefreshAndWaitingCallers(t *testing.T) {
	g := memoryGrant()
	g.ExpiresAt = time.Now().Add(time.Second)
	s, _ := NewMemorySession(g)
	entered := make(chan struct{})
	done := make(chan error, 2)
	refresh := func(ctx context.Context, current GoogleCredential) (GoogleCredential, error) {
		close(entered)
		<-ctx.Done()
		return GoogleCredential{}, ctx.Err()
	}
	go func() { _, err := s.Credential(context.Background(), refresh); done <- err }()
	<-entered
	go func() { _, err := s.Credential(context.Background(), refresh); done <- err }()
	s.Close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("closed session returned credential")
			}
		case <-time.After(time.Second):
			t.Fatal("close did not release session")
		}
	}
	if s.state.grant != nil {
		t.Fatal("grant retained after close")
	}
}

func TestMemoryCancelledWaitDoesNotCancelOtherRefresh(t *testing.T) {
	g := memoryGrant()
	g.ExpiresAt = time.Now().Add(time.Second)
	s, _ := NewMemorySession(g)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	refresh := func(ctx context.Context, current GoogleCredential) (GoogleCredential, error) {
		close(entered)
		<-release
		current.ExpiresAt = time.Now().Add(time.Hour)
		return current, nil
	}
	go func() { _, err := s.Credential(context.Background(), refresh); done <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Credential(ctx, refresh); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil || !s.Snapshot().Authorized {
		t.Fatal("one cancelled waiter cancelled session", err)
	}
}

func TestMemorySessionRejectsInvalidGrants(t *testing.T) {
	for _, mutate := range []func(*GoogleCredential){func(g *GoogleCredential) { g.RefreshToken = "" }, func(g *GoogleCredential) { g.Scope = "youtube.upload" }, func(g *GoogleCredential) { g.ChannelID = "invalid" }, func(g *GoogleCredential) { g.ClientID = "" }, func(g *GoogleCredential) { g.ExpiresAt = time.Time{} }, func(g *GoogleCredential) { g.ExpiresAt = time.Now().Add(25 * time.Hour) }} {
		g := memoryGrant()
		mutate(&g)
		if _, err := NewMemorySession(g); err == nil {
			t.Fatal("invalid grant accepted")
		}
	}
	g := memoryGrant()
	g.Scope = googleManageScope
	s, err := NewMemorySession(g)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.Credential(context.Background(), nil)
	if err != nil || cred.Scope != googleManageScope {
		t.Fatal("explicit device edit scope rejected", err)
	}
	g.ExpiresAt = time.Now()
	s, _ = NewMemorySession(g)
	if _, err = s.Credential(context.Background(), nil); videos.SafeFailure(err).Code != "authentication_required" || s.Snapshot().Authorized {
		t.Fatal("expired grant without refresh accepted", err)
	}
}
