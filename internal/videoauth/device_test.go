package videoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type deviceClock struct {
	mu     sync.Mutex
	at     time.Time
	delays []time.Duration
}

func (c *deviceClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *deviceClock) wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
	c.delays = append(c.delays, d)
	return nil
}

func deviceConfig() DeviceConfig {
	return DeviceConfig{ClientID: "device-client", ClientSecret: "device-client-SECRET", ChannelID: oauthFixtureChannel, Access: "read"}
}
func deviceBeginResponse(w http.ResponseWriter, expires int, interval int) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"device_code": "fixture-device-SECRET", "user_code": "GQVQ-JKEC", "verification_url": "https://www.google.com/device", "expires_in": expires, "interval": interval})
}
func deviceClient(t *testing.T, handler http.HandlerFunc) (*GoogleDevice, *deviceClock) {
	t.Helper()
	d, err := NewGoogleDevice(deviceConfig())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	d.http.Transport = srv.Client().Transport
	d.deviceURL = srv.URL + "/device"
	d.tokenURL = srv.URL + "/token"
	d.channelsURL = srv.URL + "/channels"
	clock := &deviceClock{at: time.Now()}
	d.now = clock.now
	d.wait = clock.wait
	return d, clock
}

func TestDevicePendingSlowDownAndExactGrant(t *testing.T) {
	polls := 0
	d, clock := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/device":
			if r.Method != "POST" || r.Form.Get("client_id") != "device-client" || r.Form.Get("scope") != youtube.ReadScope || r.Form.Has("client_secret") {
				t.Error("device begin contract")
			}
			deviceBeginResponse(w, 1800, 5)
		case "/token":
			polls++
			if r.Form.Get("client_secret") != "device-client-SECRET" || r.Form.Get("device_code") != "fixture-device-SECRET" || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("poll contract")
			}
			w.Header().Set("Content-Type", "application/json")
			switch polls {
			case 1:
				w.WriteHeader(428)
				w.Write([]byte(`{"error":"authorization_pending"}`))
			case 2:
				w.WriteHeader(403)
				w.Write([]byte(`{"error":"slow_down"}`))
			case 3:
				w.WriteHeader(428)
				w.Write([]byte(`{"error":"authorization_pending"}`))
			default:
				validToken(w, youtube.ReadScope)
			}
		case "/channels":
			if r.Header.Get("Authorization") != "Bearer fixture-access-SECRET" || r.URL.Query().Get("mine") != "true" {
				t.Error("channel verification")
			}
			channelResponse(w, oauthFixtureChannel)
		}
	})
	s, err := d.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := s.Presentation()
	if p.UserCode != "GQVQ-JKEC" || p.VerificationURL != "https://www.google.com/device" || p.ExpiresAt.IsZero() {
		t.Fatal("unsafe presentation")
	}
	if strings.Contains(fmt.Sprintf("%v %#v %v %#v %v %#v", d, d, s, s, *s, *s), "SECRET") {
		t.Fatal("device code or client secret formatted")
	}
	grant, err := d.Poll(context.Background(), s)
	if err != nil || grant.ChannelID != oauthFixtureChannel || grant.Scope != youtube.ReadScope || grant.RefreshToken == "" {
		t.Fatal("grant failed", err)
	}
	if fmt.Sprint(clock.delays) != "[5s 5s 10s 10s]" || polls != 4 {
		t.Fatal("poll interval/slow_down ignored", clock.delays, polls)
	}
	if _, err = d.Poll(context.Background(), s); err == nil || polls != 4 {
		t.Fatal("session polled again")
	}
}

func TestDeviceScopeConfigAndPresentationValidation(t *testing.T) {
	for _, access := range []string{"", "force-ssl", youtube.EditScope, "read edit"} {
		c := deviceConfig()
		c.Access = access
		if _, err := NewGoogleDevice(c); err == nil {
			t.Fatal("unsupported device scope accepted")
		}
	}
	c := deviceConfig()
	c.Access = "edit"
	d, err := NewGoogleDevice(c)
	if err != nil || d.scope != googleManageScope {
		t.Fatal("edit device scope selection", err)
	}
	if d.deviceURL != googleDeviceURL || d.tokenURL != googleTokenURL || d.channelsURL != googleChannelsURL {
		t.Fatal("production endpoint override")
	}
	for _, bad := range []map[string]any{
		{"verification_url": "https://evil.example.invalid/device"}, {"verification_url": "https://google.com.evil.example.invalid/device"}, {"verification_url": "http://www.google.com/device"}, {"verification_url": "https://user@www.google.com/device"}, {"verification_url": "https://www.google.com/device?device_code=SECRET"}, {"verification_url": "https://www.google.com/device#SECRET"}, {"user_code": "SECRET\n"}, {"device_code": ""}, {"interval": 0}, {"interval": 61}, {"expires_in": 0}, {"expires_in": 3601},
	} {
		d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
			v := map[string]any{"device_code": "fixture-device-SECRET", "user_code": "USER-CODE", "verification_url": "https://www.google.com/device", "expires_in": 1800, "interval": 5}
			for k, x := range bad {
				v[k] = x
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(v)
		})
		if s, err := d.Begin(context.Background()); err == nil || s != nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("bad authorization response accepted")
		}
	}
	for _, raw := range []string{`null`, `{"device_code":"a","device_code":"b"}`, strings.Repeat(" ", 16385)} {
		d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(raw))
		})
		if _, err := d.Begin(context.Background()); err == nil {
			t.Fatal("malformed device response accepted")
		}
	}
}

func TestDeviceTerminalErrorsNoReplayOrRedirect(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{403, `{"error":"access_denied","error_description":"SECRET"}`}, {400, `{"error":"expired_token"}`}, {401, `{"error":"invalid_client"}`}, {400, `{"error":"unauthorized_client"}`}, {429, `{"error":"rate_limit_exceeded"}`}, {503, `{"error":"authorization_pending"}`}, {500, `{"error":"slow_down"}`}, {428, `{"error":"authorization_pending","access_token":"SECRET"}`}, {302, `{}`}, {200, `{"access_token":"SECRET"}`}, {403, `{"error":"slow_down","error":"authorization_pending"}`},
	} {
		t.Run(fmt.Sprint(tc.status)+tc.body[:min(15, len(tc.body))], func(t *testing.T) {
			polls := 0
			d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device" {
					deviceBeginResponse(w, 1800, 5)
					return
				}
				polls++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "https://never-follow.example.invalid/token")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			s, err := d.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			grant, err := d.Poll(context.Background(), s)
			if err == nil || grant.AccessToken != "" || polls != 1 || videos.SafeFailure(err).Retryable || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe terminal result", err, polls)
			}
			if _, err = d.Poll(context.Background(), s); err == nil || polls != 1 {
				t.Fatal("terminal device code replayed")
			}
		})
	}
}

func TestDeviceExpiryCancellationAndNoBusyPolling(t *testing.T) {
	polls := 0
	d, clock := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device" {
			deviceBeginResponse(w, 12, 5)
			return
		}
		polls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(428)
		w.Write([]byte(`{"error":"authorization_pending"}`))
	})
	s, _ := d.Begin(context.Background())
	if _, err := d.Poll(context.Background(), s); videos.SafeFailure(err).Code != "device_authorization_expired" || polls != 2 || fmt.Sprint(clock.delays) != "[5s 5s]" {
		t.Fatal("lifetime not bounded", err, polls, clock.delays)
	}
	s, _ = d.Begin(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	d.wait = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	if _, err := d.Poll(ctx, s); !errors.Is(err, context.Canceled) || polls != 2 {
		t.Fatal("cancelled wait polled", err)
	}
	d.wait = func(context.Context, time.Duration) error { return nil }
	s, _ = d.Begin(context.Background())
	if _, err := d.Poll(context.Background(), s); videos.SafeFailure(err).Code != "device_poll_timing" || polls != 2 {
		t.Fatal("early polling allowed", err)
	}
	if _, err := d.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled begin reached network", err)
	}
}

func TestDeviceGrantScopeChannelAndConcurrency(t *testing.T) {
	for _, scenario := range []string{"scope", "channel", "edit"} {
		t.Run(scenario, func(t *testing.T) {
			var polls atomic.Int32
			d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/device":
					deviceBeginResponse(w, 1800, 5)
				case "/token":
					polls.Add(1)
					scope := youtube.ReadScope
					if scenario == "scope" {
						scope = youtube.EditScope
					}
					if scenario == "edit" {
						scope = googleManageScope
					}
					validToken(w, scope)
				case "/channels":
					id := oauthFixtureChannel
					if scenario == "channel" {
						id = "UC1234567890123456789012"
					}
					channelResponse(w, id)
				}
			})
			if scenario == "edit" {
				d.scope = googleManageScope
			}
			s, _ := d.Begin(context.Background())
			var successes atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					g, err := d.Poll(context.Background(), s)
					if err == nil && g.AccessToken != "" {
						successes.Add(1)
					}
				}()
			}
			wg.Wait()
			if polls.Load() != 1 {
				t.Fatal("concurrent session replay", polls.Load())
			}
			want := int32(0)
			if scenario == "edit" {
				want = 1
			}
			if successes.Load() != want {
				t.Fatal("scope/channel bypass", successes.Load())
			}
		})
	}
}

func TestDeviceDefaultIntervalAndPrintablePresentation(t *testing.T) {
	d, clock := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"device_code":"device-SECRET","user_code":"Case Sensitive 'Code'","verification_uri":"https://accounts.google.com/device/custom","expires_in":1800}`))
		case "/token":
			validToken(w, youtube.ReadScope)
		case "/channels":
			channelResponse(w, oauthFixtureChannel)
		}
	})
	s, err := d.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Presentation(); p.UserCode != "Case Sensitive 'Code'" || p.VerificationURL != "https://accounts.google.com/device/custom" {
		t.Fatal("presentation altered")
	}
	if _, err = d.Poll(context.Background(), s); err != nil || fmt.Sprint(clock.delays) != "[5s]" {
		t.Fatal("default interval failed", err, clock.delays)
	}
}

func TestDeviceNetworkTimeoutAndMissingScopeAreTerminal(t *testing.T) {
	for _, scenario := range []string{"timeout", "missing-scope", "mixed-error"} {
		t.Run(scenario, func(t *testing.T) {
			var polls atomic.Int32
			d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device" {
					deviceBeginResponse(w, 1800, 5)
					return
				}
				polls.Add(1)
				if scenario == "timeout" {
					time.Sleep(30 * time.Millisecond)
					validToken(w, youtube.ReadScope)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				v := map[string]any{"access_token": "access-SECRET", "refresh_token": "refresh-SECRET", "token_type": "Bearer", "expires_in": 3600}
				if scenario == "mixed-error" {
					v["scope"] = youtube.ReadScope
					v["error"] = "authorization_pending"
				}
				json.NewEncoder(w).Encode(v)
			})
			s, err := d.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "timeout" {
				d.http.Timeout = time.Millisecond
			}
			if g, err := d.Poll(context.Background(), s); err == nil || g.AccessToken != "" {
				t.Fatal("uncertain exchange returned grant")
			}
			count := polls.Load()
			if _, err = d.Poll(context.Background(), s); err == nil || polls.Load() != count {
				t.Fatal("uncertain session replayed")
			}
		})
	}
}

func TestDeviceBeginNoRetryRedirect(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			d, _ := deviceClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://never-follow.example.invalid/device")
				w.WriteHeader(status)
				w.Write([]byte("SECRET"))
			})
			if s, err := d.Begin(context.Background()); err == nil || s != nil || calls != 1 || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe begin failure", err, calls)
			}
		})
	}
}
