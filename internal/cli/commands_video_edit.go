package cli

import (
	"context"
	"encoding/json"
	"flag"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryhttp"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videoauth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type videoEdit func(context.Context, string, youtube.Patch, bool, string, string, videoauth.Credentials) (youtube.EditResult, error)

func editVideo(ctx context.Context, id string, patch youtube.Patch, apply bool, etag, hash string, credentials videoauth.Credentials) (youtube.EditResult, error) {
	grant := credentials.YouTube
	if grant == nil {
		return youtube.EditResult{}, videos.FailureCode("authentication_required")
	}
	if grant.Scope != youtube.EditScope {
		return youtube.EditResult{}, videos.FailureCode("permission_denied")
	}
	client := queryhttp.New(youtube.Origin)
	client.Permit = credentials.CheckYouTube
	provider := &youtube.Provider{Client: client, Grant: youtube.Grant{Bearer: grant.AccessToken, Scope: grant.Scope, ChannelID: grant.ChannelID}}
	if apply {
		return provider.ApplyEdit(ctx, id, patch, etag, hash)
	}
	plan, err := provider.PlanEdit(ctx, id, patch)
	return youtube.EditResult{EditPlan: plan, DryRun: true, Outcome: "planned"}, err
}

func runVideoEdit(ctx context.Context, args []string, c *commandContext) int {
	c.result.Command = "videos edit"
	if len(args) == 0 || !strings.HasPrefix(args[0], "youtube:") || !videos.ValidYouTubeID(strings.TrimPrefix(args[0], "youtube:")) {
		return c.finish(videos.ErrInput)
	}
	id := strings.TrimPrefix(args[0], "youtube:")
	fs := c.flags()
	title := fs.String("title", "", "")
	description := fs.String("description", "", "")
	tagsJSON := fs.String("tags-json", "", "")
	apply := fs.Bool("apply", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	etag := fs.String("if-match", "", "")
	hash := fs.String("plan-hash", "", "")
	credentialsStdin := fs.Bool("credentials-stdin", false, "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	if err := c.parse(fs, args[1:]); err != nil {
		return c.finish(err)
	}
	if *apply && *dryRun || *timeout <= 0 || *timeout > 30*time.Second || *apply && (*etag == "" || len(*hash) != 64) || !*apply && (*etag != "" || *hash != "") {
		return c.finish(videos.ErrInput)
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	patch := youtube.Patch{}
	if provided["title"] {
		patch.Title = title
	}
	if provided["description"] {
		patch.Description = description
	}
	if provided["tags-json"] {
		var tags []string
		if !strings.HasPrefix(strings.TrimSpace(*tagsJSON), "[") || json.Unmarshal([]byte(*tagsJSON), &tags) != nil {
			return c.finish(videos.ErrInput)
		}
		patch.Tags = &tags
	}
	if err := patch.Validate(); err != nil {
		return c.finish(err)
	}
	if !*credentialsStdin {
		return c.finish(videos.FailureCode("authentication_required"))
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	credentials, err := videoauth.Read(ctx, c.input)
	if err != nil {
		return c.finish(err)
	}
	if credentials.YouTube == nil || credentials.YouTube.Scope != youtube.EditScope {
		return c.finish(videos.FailureCode("permission_denied"))
	}
	run := editVideo
	if c.videoEdit != nil {
		run = c.videoEdit
	}
	c.result.Data, err = run(ctx, id, patch, *apply, *etag, *hash, credentials)
	return c.finish(err)
}
