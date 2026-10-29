package videoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

func descriptor() Credentials {
	return Credentials{YouTube: &YouTubeCredential{AccessToken: "fixture-token-SECRET", Scope: youtube.ReadScope, ChannelID: "UCabcdefghijklmnopqrstuv", ExpiresAt: time.Now().Add(time.Hour)}, HHC: &HHCCredential{AccessToken: "hhc-fixture-SECRET", Scope: "cms:pages:read offline_access", PrincipalType: "human", ExpiresAt: time.Now().Add(time.Hour)}}
}

func readPipe(t *testing.T, data []byte) (Credentials, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan struct{})
	go func() { defer close(done); defer w.Close(); _, _ = w.Write(data) }()
	c, e := Read(context.Background(), r)
	r.Close()
	<-done
	return c, e
}

func TestPrivatePipeCredentialsAndFormatting(t *testing.T) {
	raw, _ := json.Marshal(descriptor())
	c, err := readPipe(t, raw)
	if err != nil || c.YouTube.AccessToken != "fixture-token-SECRET" || c.HHC.PrincipalType != "human" {
		t.Fatal("valid injection rejected", err)
	}
	for _, v := range []any{c, *c.YouTube, *c.HHC, GoogleCredential{RefreshToken: "SECRET"}} {
		if strings.Contains(fmt.Sprintf("%v %#v", v, v), "SECRET") {
			t.Fatal("formatted credentials exposed")
		}
	}
	if c.CheckYouTube(context.Background(), "youtube.search", 1) != nil || c.CheckHHC(context.Background(), "hhc.read", 1) != nil {
		t.Fatal("source checks failed")
	}
	if (Credentials{HHC: c.HHC}).CheckYouTube(context.Background(), "", 0) == nil {
		t.Fatal("HHC credential authorized YouTube")
	}
	c.YouTube.ExpiresAt = time.Now().Add(-time.Second)
	if videos.SafeFailure(c.CheckYouTube(context.Background(), "", 0)).Code != "authentication_required" {
		t.Fatal("expired credential authorized")
	}
	if c.CheckHHC(context.Background(), "", 0) != nil {
		t.Fatal("YouTube expiry changed HHC check")
	}
}

func TestFilesNilAndCancelledPipeRejected(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "descriptor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, _ := json.Marshal(descriptor())
	f.Write(raw)
	f.Seek(0, 0)
	if _, err = Read(context.Background(), f); !errors.Is(err, videos.ErrInput) {
		t.Fatal("regular file accepted", err)
	}
	if _, err = Read(context.Background(), nil); !errors.Is(err, videos.ErrInput) {
		t.Fatal("nil accepted", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := Read(ctx, r); done <- e }()
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("pipe cancellation hung")
	}
}

func TestStrictDescriptorValidation(t *testing.T) {
	valid, _ := json.Marshal(descriptor())
	for _, raw := range []string{"null", "{}", "[]", `{"youtube":null}`, `{"youtube":{},"youtube":{}}`, string(valid) + `{}`, `{"unknown":"SECRET"}`, strings.Repeat(" ", 16385), strings.Replace(string(valid), `"channelId":`, `"unknown":`, 1), strings.Replace(string(valid), `"accessToken":`, `"accessToken":"duplicate-SECRET","accessToken":`, 1)} {
		_, e := readPipe(t, []byte(raw))
		if e == nil || strings.Contains(e.Error(), "SECRET") {
			t.Fatal("malformed injection accepted or echoed")
		}
	}
	for _, mutate := range []func(*Credentials){
		func(c *Credentials) { c.YouTube.Scope = "youtube.upload" },
		func(c *Credentials) { c.YouTube.ChannelID = "wrong" },
		func(c *Credentials) { c.YouTube.AccessToken = "token\r\nSECRET" },
		func(c *Credentials) { c.YouTube.ExpiresAt = time.Now().Add(-time.Hour) },
		func(c *Credentials) { c.YouTube.ExpiresAt = time.Now().Add(25 * time.Hour) },
		func(c *Credentials) { c.HHC.PrincipalType = "service" },
		func(c *Credentials) { c.HHC.Scope = "cms:pages:read cms:pages:write" },
		func(c *Credentials) { c.HHC.Scope = "cms:pages:read cms:pages:read" },
	} {
		c := descriptor()
		mutate(&c)
		raw, _ := json.Marshal(c)
		if _, e := readPipe(t, raw); e == nil {
			t.Fatal("invalid credential accepted")
		}
	}
}
