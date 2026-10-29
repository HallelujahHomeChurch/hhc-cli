package cli

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videoauth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/hhc"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type videoQuery func(context.Context, string, videos.Input) (videos.Result, error)

func (c *commandContext) queryVideos(ctx context.Context, operation string, in videos.Input, credentials videoauth.Credentials) (videos.Result, error) {
	if c.videoQuery != nil {
		return c.videoQuery(ctx, operation, in)
	}
	return videoService(credentials).Execute(ctx, operation, in)
}

// The executor injects credentials for this invocation only. Native recording
// profiles, personal keychains and media startup are never used by queries.
func videoService(credentials videoauth.Credentials) videos.Service {
	service := videos.Service{HHC: &hhc.Provider{Client: queryhttp.New(hhc.PublicOrigin)}}
	channel, key := os.Getenv("HHC_YOUTUBE_CHANNEL_ID"), os.Getenv("HHC_YOUTUBE_API_KEY")
	if channel != "" && key != "" {
		service.YouTube = &youtube.Provider{Client: queryhttp.New(youtube.Origin), Grant: youtube.Grant{APIKey: key, ChannelID: channel}}
	}
	if grant := credentials.YouTube; grant != nil {
		client := queryhttp.New(youtube.Origin)
		client.Permit = credentials.CheckYouTube
		service.YouTube = &youtube.Provider{Client: client, Grant: youtube.Grant{Bearer: grant.AccessToken, Scope: grant.Scope, ChannelID: grant.ChannelID}}
	}
	if grant := credentials.HHC; grant != nil {
		client := queryhttp.New(hhc.AdminOrigin)
		client.Permit = credentials.CheckHHC
		service.HHC = &hhc.Provider{Client: client, PublicClient: queryhttp.New(hhc.PublicOrigin), Grant: hhc.Grant{Human: true, Scope: grant.Scope, Bearer: grant.AccessToken}}
	}
	return service
}
func runVideos(ctx context.Context, args []string, c *commandContext) int {
	if len(args) == 0 {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	operation := ""
	in := videos.Input{}
	rest := args[1:]
	switch args[0] {
	case "search":
		operation = "search"
	case "get":
		operation = "get"
		if len(rest) == 0 {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		in.ID = rest[0]
		rest = rest[1:]
	case "compare":
		operation = "compare"
		if len(rest) == 0 {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		in.YouTubeVideoID = rest[0]
		rest = rest[1:]
	default:
		return c.finish(auth.ErrInvalidAuthInput)
	}
	c.result.Command = "videos " + args[0]
	fs := c.flags()
	credentialsStdin := fs.Bool("credentials-stdin", false, "")
	if operation == "search" {
		fs.StringVar(&in.Query, "query", "", "")
		fs.StringVar(&in.Source, "source", "all", "")
	}
	fs.IntVar(&in.Limit, "limit", 20, "")
	fs.IntVar(&in.MaxPages, "max-pages", 3, "")
	fs.StringVar(&in.Locale, "locale", "zh-Hant", "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	if err := c.parse(fs, rest); err != nil {
		return c.finish(err)
	}
	if *timeout <= 0 || *timeout > 30*time.Second {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	if _, err := in.Normalize(); err != nil {
		return c.finish(err)
	}
	if operation == "get" {
		source, id, ok := strings.Cut(in.ID, ":")
		if !ok || source != "hhc" && source != "youtube" || source == "hhc" && !videos.ValidHHCID(id) || source == "youtube" && !videos.ValidYouTubeID(id) {
			return c.finish(videos.ErrInput)
		}
	}
	if operation == "compare" && !videos.ValidYouTubeID(in.YouTubeVideoID) {
		return c.finish(videos.ErrInput)
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var credentials videoauth.Credentials
	if *credentialsStdin {
		credentials, err = videoauth.Read(ctx, c.input)
		if err != nil {
			return c.finish(err)
		}
	}
	c.result.Data, err = c.queryVideos(ctx, operation, in, credentials)
	return c.finish(err)
}
