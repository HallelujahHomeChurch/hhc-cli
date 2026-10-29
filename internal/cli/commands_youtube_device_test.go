package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videoauth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

const deviceFixtureChannel = "UCabcdefghijklmnopqrstuv"

func deviceFixtureLogin(_ context.Context, c videoauth.DeviceConfig, present func(videoauth.DevicePresentation)) (videoauth.GoogleCredential, error) {
	present(videoauth.DevicePresentation{VerificationURL: "https://www.google.com/device", UserCode: "ABCD-EFGH", ExpiresAt: time.Now().Add(time.Minute)})
	scope := youtube.ReadScope
	if c.Access == "edit" {
		scope = youtube.ManageScope
	}
	return videoauth.GoogleCredential{ClientID: c.ClientID, ClientSecret: c.ClientSecret, ChannelID: c.ChannelID, Scope: scope, AccessToken: "ACCESS-SECRET", RefreshToken: "REFRESH-SECRET", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func deviceArgs(session bool) []string {
	args := []string{"--client-id", "fixture-client", "--channel-id", deviceFixtureChannel, "--access", "edit", "--json"}
	if session {
		args = append(args, "--session")
	}
	return args
}
func assertNoDeviceSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{"CLIENT-SECRET", "ACCESS-SECRET", "REFRESH-SECRET", "DEVICE-SECRET"} {
		if strings.Contains(text, secret) {
			t.Fatal("device credential exposed")
		}
	}
}

func TestDeviceLoginRunsOneRequestedCommandWithoutPersisting(t *testing.T) {
	t.Setenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET", "CLIENT-SECRET")
	var out, diag bytes.Buffer
	called := false
	c := &commandContext{output: &out, diagnostics: &diag, result: result{SchemaVersion: 1}, deviceLogin: deviceFixtureLogin, videoQuery: func(_ context.Context, operation string, input videos.Input) (videos.Result, error) {
		called = true
		if operation != "get" || input.ID != "youtube:abcdefghijk" {
			t.Error("requested command changed")
		}
		return videos.Result{}, nil
	}}
	args := append(deviceArgs(false), "--", "videos", "get", "youtube:abcdefghijk")
	if exit := runYouTubeDevice(context.Background(), args, c); exit != 0 || !called {
		t.Fatal(exit, out.String())
	}
	var result result
	if json.Unmarshal(out.Bytes(), &result) != nil || !result.OK || result.Command != "videos get" {
		t.Fatal("one-shot output is not one command result", out.String())
	}
	if !strings.Contains(diag.String(), "https://www.google.com/device") || !strings.Contains(diag.String(), "ABCD-EFGH") || !strings.Contains(diag.String(), youtube.ManageScope) {
		t.Fatal("consent instructions absent")
	}
	if os.Getenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET") != "" {
		t.Fatal("client secret retained for child inheritance")
	}
	assertNoDeviceSecrets(t, out.String()+diag.String())
}
func TestDeviceSessionUsesMemoryAndFencesUncertainEdits(t *testing.T) {
	t.Setenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET", "CLIENT-SECRET")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	commands := [][]string{{"videos", "get", "youtube:abcdefghijk"}, {"videos", "edit", "youtube:abcdefghijk", "--title", "New", "--apply", "--if-match", "old", "--plan-hash", strings.Repeat("0", 64)}, {"videos", "get", "youtube:abcdefghijk"}, {"videos", "edit", "youtube:abcdefghijk", "--title", "New", "-apply", "--if-match", "old", "--plan-hash", strings.Repeat("0", 64)}, {"exit"}}
	go func() {
		defer w.Close()
		e := json.NewEncoder(w)
		for _, argv := range commands {
			_ = e.Encode(argv)
		}
	}()
	var out, diag bytes.Buffer
	queries, edits := 0, 0
	c := &commandContext{input: r, output: &out, diagnostics: &diag, result: result{SchemaVersion: 1}, deviceLogin: deviceFixtureLogin, videoQuery: func(context.Context, string, videos.Input) (videos.Result, error) {
		queries++
		return videos.Result{}, nil
	}, videoEdit: func(_ context.Context, _ string, _ youtube.Patch, apply bool, _ string, _ string, credentials videoauth.Credentials) (youtube.EditResult, error) {
		edits++
		if !apply || credentials.YouTube.Scope != youtube.ManageScope {
			t.Error("edit grant/intent changed")
		}
		return youtube.EditResult{Outcome: "uncertain"}, videos.FailureCode("mutation_uncertain")
	}}
	if exit := runYouTubeDevice(context.Background(), deviceArgs(true), c); exit != 0 {
		t.Fatal(exit, out.String())
	}
	if queries != 2 || edits != 1 {
		t.Fatal("uncertain write replayed or read blocked", queries, edits)
	}
	assertNoDeviceSecrets(t, out.String()+diag.String())
	d := json.NewDecoder(&out)
	count := 0
	for {
		var v map[string]any
		err := d.Decode(&v)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 5 {
		t.Fatal("wrong NDJSON result count", count)
	}
	assertNoDeviceSecrets(t, out.String()+diag.String())
}
func TestDevicePreflightDoesNotStartConsentForMissingConfigOrUnsafeCommand(t *testing.T) {
	for _, args := range [][]string{deviceArgs(false), append(deviceArgs(false), "--", "recordings", "publish", "unsafe"), append(deviceArgs(false), "--", "videos", "get", "invalid-id"), append(deviceArgs(false), "--", "videos", "get", "youtube:abcdefghijk", "--credentials-stdin")} {
		t.Setenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET", "CLIENT-SECRET")
		var out bytes.Buffer
		called := false
		c := &commandContext{output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}, deviceLogin: func(context.Context, videoauth.DeviceConfig, func(videoauth.DevicePresentation)) (videoauth.GoogleCredential, error) {
			called = true
			return videoauth.GoogleCredential{}, nil
		}}
		if exit := runYouTubeDevice(context.Background(), args, c); exit != 2 || called {
			t.Fatal("unsafe command started login", args, exit, called)
		}
	}
	t.Setenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET", "")
	var out bytes.Buffer
	c := &commandContext{output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}}
	if exit := runYouTubeDevice(context.Background(), append(deviceArgs(false), "--", "videos", "get", "youtube:abcdefghijk"), c); exit != 3 || !strings.Contains(out.String(), "device_client_not_configured") {
		t.Fatal(exit, out.String())
	}
}

func TestDeviceSessionInputCancellationAndNoShell(t *testing.T) {
	t.Setenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET", "CLIENT-SECRET")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var out bytes.Buffer
	c := &commandContext{input: r, output: &out, diagnostics: io.Discard, result: result{SchemaVersion: 1}, deviceLogin: deviceFixtureLogin}
	args := append(deviceArgs(true), "--session-timeout", "20ms")
	done := make(chan int, 1)
	go func() { done <- runYouTubeDevice(context.Background(), args, c) }()
	select {
	case exit := <-done:
		if exit != 3 {
			t.Fatal("session timeout missing", exit, out.String())
		}
	case <-time.After(time.Second):
		t.Fatal("blocked session input did not cancel")
	}
	for _, argv := range [][]string{{"sh", "-c", "anything"}, {"videos", "get", "youtube:abcdefghijk", "--credentials-stdin"}, {"auth", "login"}} {
		if validDeviceCommand(argv) {
			t.Fatal("session accepts unsafe command")
		}
	}
}

func TestDeviceArgvRejectsNullAndNonStringMetadata(t *testing.T) {
	for _, raw := range []string{`["videos","edit","youtube:abcdefghijk","--description",null]`, `["videos",1]`, `null`, `[]`, `["exit"]{}`, string([]byte{'[', '"', 0xff, '"', ']'})} {
		if _, err := decodeDeviceArgv([]byte(raw)); err == nil {
			t.Fatal("ambiguous session argv accepted")
		}
	}
	if argv, err := decodeDeviceArgv([]byte(`["videos","edit","youtube:abcdefghijk","--description",""]`)); err != nil || argv[4] != "" {
		t.Fatal("explicit empty description rejected")
	}
}
