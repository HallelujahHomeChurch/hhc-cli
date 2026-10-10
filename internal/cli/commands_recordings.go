package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
	"golang.org/x/term"
)

func runRecordings(ctx context.Context, args []string, c *commandContext) int {
	if len(args) < 2 || !slices.Contains([]string{"get", "upload", "resume", "publish", "prepare"}, args[0]) {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	r := &c.result
	r.Command = "recordings " + args[0]
	recordingInput, recordingID := args[1], args[1]
	var recordingOutput, operationID, title, coverPath string
	var publish, prepare, explicitProfile, noninteractive bool
	timeout := 4 * time.Hour
	if args[0] == "get" || args[0] == "publish" {
		if !api.ValidRecordingID(recordingID) {
			return c.finish(auth.ErrInvalidAuthInput)
		}
	}
	if args[0] == "resume" {
		operationID = recordingInput
	}
	if args[0] == "publish" {
		publish = true
	}
	fs := c.flags()
	profile := "default"
	if args[0] != "prepare" {
		fs.StringVar(&profile, "profile", "default", "")
	}
	if r.Command == "recordings upload" {
		fs.StringVar(&title, "title", "", "")
		fs.StringVar(&operationID, "operation-id", "", "")
		fs.BoolVar(&publish, "publish", false, "")
		fs.BoolVar(&prepare, "prepare", false, "")
		fs.StringVar(&coverPath, "cover", "", "")
	}
	if r.Command == "recordings publish" {
		fs.StringVar(&operationID, "operation-id", "", "")
	}
	if r.Command == "recordings prepare" {
		fs.StringVar(&operationID, "operation-id", "", "")
		fs.StringVar(&recordingOutput, "output", "", "")
	}
	if r.Command == "recordings upload" || r.Command == "recordings resume" || r.Command == "recordings publish" || r.Command == "recordings prepare" {
		fs.DurationVar(&timeout, "timeout", 4*time.Hour, "")
	}
	if err := c.parse(fs, args[2:]); err != nil {
		return c.finish(err)
	}
	unlock, err := lockInstallation()
	if err != nil {
		return c.finish(err)
	}
	defer unlock()
	input, diagnostics, jsonMode, noInput := c.input, c.diagnostics, c.jsonMode, &c.noInput
	if r.Command == "recordings upload" || r.Command == "recordings resume" || r.Command == "recordings publish" || r.Command == "recordings prepare" {
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "profile" {
				explicitProfile = true
			}
		})
		noninteractive = *noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd()))
		needsProfile := r.Command == "recordings upload" || r.Command == "recordings publish"
		if timeout <= 0 || noninteractive && (needsProfile && !explicitProfile || operationID == "") || r.Command == "recordings upload" && strings.TrimSpace(title) == "" || r.Command == "recordings prepare" && recordingOutput == "" {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		if operationID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return c.finish(err)
			}
			id[6] = (id[6] & 15) | 64
			id[8] = (id[8] & 63) | 128
			operationID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		}
		if !api.ValidRecordingID(operationID) {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		r.OperationID = &operationID
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return c.finish(err)
	}
	if !auth.ValidProfile(profile) {
		return c.finish(auth.ErrInvalidAuthInput)
	}
	r.Profile = &profile
	directory, err := profileDirectory()
	if err != nil {
		return c.finish(err)
	}
	operations := filepath.Join(directory, "HHC", "cli", "operations")
	if configured := os.Getenv("HHC_CLI_OPERATIONS_DIR"); configured != "" {
		if !filepath.IsAbs(configured) {
			return c.finish(auth.ErrInvalidAuthInput)
		}
		operations = filepath.Clean(configured)
	}
	if strings.HasPrefix(r.Command, "recordings ") {
		report, sweepErr := recordings.SweepExpired(operations, time.Now().UTC())
		if sweepErr != nil || report.Failed != 0 {
			fmt.Fprintln(diagnostics, "部分已過期暫存尚未清理；未移除原始影片或使用者輸出。")
		}
	}
	if r.Command == "recordings prepare" || r.Command == "recordings resume" {
		var intent *recordings.Intent
		if r.Command == "recordings prepare" {
			source, sourceErr := filepath.Abs(recordingInput)
			output, outputErr := filepath.Abs(recordingOutput)
			if sourceErr != nil || outputErr != nil || source == output {
				return c.finish(auth.ErrInvalidAuthInput)
			}
			options := media.DefaultEncodeOptions()
			intent = &recordings.Intent{Command: "prepare", Input: source, Output: output, VideoBitrate720: options.VideoBitrate720, VideoBitrate1080: options.VideoBitrate1080}
		}
		journal, err := recordings.OpenJournal(operations, operationID, intent)
		if err != nil {
			return c.finish(err)
		}
		if journal.State().Intent.Command == "prepare" {
			defer journal.Close()
			c.progress = newProgressDisplay(diagnostics, jsonMode, operationID, !noninteractive)
			journal.Progress = c.progress.Encoding
			r.Profile = nil
			if !jsonMode {
				fmt.Fprintln(diagnostics, "Operation:", operationID)
			}
			r.Data, err = recordings.Prepare(ctx, journal)
			if err != nil {
				err = errors.Join(err, journal.Save(journal.State()))
			}
			return c.finish(err)
		}
		journal.Close()
		if noninteractive && !explicitProfile {
			return c.finish(auth.ErrInvalidAuthInput)
		}
	}
	profiles := auth.NewProfiles(filepath.Join(directory, "HHC", "cli", "profiles"))
	var token auth.Token
	switch r.Command {
	case "recordings upload", "recordings resume", "recordings publish":
		token, err = profiles.Token(ctx, profile)
		if err != nil {
			return c.finish(err)
		}
		client := api.NewClient(token, func(ctx context.Context) (auth.Token, error) { return profiles.Token(ctx, profile) })
		principal := client.Principal()
		r.Principal = &principal
		scopes := []string{"cms:recordings:read"}
		if r.Command == "recordings upload" {
			scopes = append(scopes, "cms:recordings:write")
		}
		if publish {
			scopes = append(scopes, "cms:recordings:publish")
		}
		if err := client.RequireScopes(scopes...); err != nil {
			return c.finish(err)
		}
		var intent *recordings.Intent
		if r.Command == "recordings publish" {
			intent = &recordings.Intent{Command: "publish", Profile: profile, PrincipalType: principal.Type, PrincipalID: principal.ID, ClientID: principal.ClientID, RecordingID: recordingInput, Publish: true}
		}
		if r.Command == "recordings upload" {
			path, err := filepath.Abs(recordingInput)
			if err != nil {
				return c.finish(auth.ErrInvalidAuthInput)
			}
			intent = &recordings.Intent{Command: "upload", Profile: profile, PrincipalType: principal.Type, PrincipalID: principal.ID, ClientID: principal.ClientID, Input: path, Title: strings.TrimSpace(title), Publish: publish}
			if coverPath != "" {
				intent.CoverPath, err = filepath.Abs(coverPath)
				if err != nil {
					return c.finish(auth.ErrInvalidAuthInput)
				}
			}
			if prepare {
				options := media.DefaultEncodeOptions()
				intent.Prepare, intent.VideoBitrate720, intent.VideoBitrate1080 = true, options.VideoBitrate720, options.VideoBitrate1080
			}
		}
		journal, err := recordings.OpenJournal(operations, operationID, intent)
		if err != nil {
			return c.finish(err)
		}
		defer journal.Close()
		c.progress = newProgressDisplay(diagnostics, jsonMode, operationID, !noninteractive)
		journal.Progress = c.progress.Encoding
		if c.progress.interactive {
			journal.TransferProgress = c.progress.Transfer
		}
		if journal.State().Intent.Profile != profile {
			return c.finish(recordings.ErrOperationConflict)
		}
		if !jsonMode {
			fmt.Fprintln(diagnostics, "Operation:", operationID)
		}
		if journal.State().Intent.Command == "publish" {
			value, publishErr := recordings.Publish(ctx, client, journal)
			r.Data = struct {
				RecordingID              string            `json:"recordingId"`
				Publication              api.PublishResult `json:"publication"`
				RequestedActionSatisfied bool              `json:"requestedActionSatisfied"`
			}{journal.State().RecordingID, value, publishErr == nil}
			err = publishErr
		} else {
			r.Data, err = recordings.UploadPrepared(ctx, client, recordings.NewUploader(), journal)
		}
		if err != nil {
			err = errors.Join(err, journal.Save(journal.State()))
		}
		return c.finish(err)
	case "recordings get":
		token, err = profiles.Token(ctx, profile)
		if err != nil {
			return c.finish(err)
		}
		client := api.NewClient(token, func(ctx context.Context) (auth.Token, error) { return profiles.Token(ctx, profile) })
		recording, getErr := client.GetRecording(ctx, recordingID)
		err = getErr
		if err == nil {
			r.Data = recording
		}
		principal := client.Principal()
		r.Principal = &principal
		return c.finish(err)
	}
	return c.finish(auth.ErrInvalidAuthInput)
}
