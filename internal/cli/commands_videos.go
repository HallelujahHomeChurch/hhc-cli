package cli

import (
	"context"
	"os"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/mcp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/hhc"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type videoQuery func(context.Context, string, videos.Input) (videos.Result, error)

func (c *commandContext) queryVideos(ctx context.Context, tool string, in videos.Input) (videos.Result, error) {
	if c.videoQuery != nil {
		return c.videoQuery(ctx, tool, in)
	}
	return publicVideoService().Execute(ctx, tool, in)
}

// Local queries use public catalog access only. Cloud caller identity and private
// grants belong to the external broker, never native profile/keychain startup.
func publicVideoService() videos.Service {
	service := videos.Service{HHC: &hhc.Provider{Client: queryhttp.New(hhc.PublicOrigin)}}
	channel, key := os.Getenv("HHC_YOUTUBE_CHANNEL_ID"), os.Getenv("HHC_YOUTUBE_API_KEY")
	if channel != "" && key != "" {
		service.YouTube = &youtube.Provider{Client: queryhttp.New(youtube.Origin), Grant: youtube.Grant{APIKey: key, ChannelID: channel}}
	}
	return service
}
func runVideos(ctx context.Context, args []string, c *commandContext) int {
	if len(args) == 0 {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	tool := ""
	in := videos.Input{}
	rest := args[1:]
	switch args[0] {
	case "search":
		tool = "search_videos"
	case "get":
		tool = "get_video"
		if len(rest) == 0 {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		in.ID = rest[0]
		rest = rest[1:]
	case "compare":
		tool = "compare_publication"
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
	if tool == "search_videos" {
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
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	c.result.Data, err = c.queryVideos(ctx, tool, in)
	return c.finish(err)
}
func runMCP(ctx context.Context, args []string, c *commandContext) int {
	if len(args) == 0 || args[0] != "serve" {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	fs := c.flags()
	stdio := fs.Bool("stdio", false, "")
	if c.parse(fs, args[1:]) != nil || !*stdio || c.jsonMode || c.input == nil {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	// stdout carries MCP messages exclusively; never append the CLI envelope.
	if mcp.ServeStdio(ctx, c.input, c.output, c.queryVideos, c.version) != nil {
		return 1
	}
	return 0
}
