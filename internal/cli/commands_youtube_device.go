package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videoauth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

type videoCredential func(context.Context) (videoauth.Credentials, error)
type deviceLogin func(context.Context, videoauth.DeviceConfig, func(videoauth.DevicePresentation)) (videoauth.GoogleCredential, error)
type videoRefresh func(context.Context, videoauth.GoogleCredential) (videoauth.GoogleCredential, error)

func loginYouTubeDevice(ctx context.Context, config videoauth.DeviceConfig, present func(videoauth.DevicePresentation)) (videoauth.GoogleCredential, error) {
	device, err := videoauth.NewGoogleDevice(config)
	if err != nil {
		return videoauth.GoogleCredential{}, err
	}
	session, err := device.Begin(ctx)
	if err != nil {
		return videoauth.GoogleCredential{}, err
	}
	present(session.Presentation())
	return device.Poll(ctx, session)
}

// Device login owns the grant only in this process. A session executes a
// bounded sequence of video argv arrays; there is no socket, daemon or file store.
func runYouTubeDevice(ctx context.Context, args []string, c *commandContext) int {
	c.result.Command = "auth youtube-device"
	var command []string
	for i, arg := range args {
		if arg == "--" {
			command = append([]string(nil), args[i+1:]...)
			args = args[:i]
			break
		}
	}
	fs := c.flags()
	clientID := fs.String("client-id", "", "")
	channel := fs.String("channel-id", "", "")
	access := fs.String("access", "read", "")
	sessionMode := fs.Bool("session", false, "")
	loginTimeout := fs.Duration("login-timeout", 15*time.Minute, "")
	sessionTimeout := fs.Duration("session-timeout", time.Hour, "")
	if err := c.parse(fs, args); err != nil {
		return c.finish(err)
	}
	if *clientID == "" || !youtube.ValidChannel(*channel) || *access != "read" && *access != "edit" || *loginTimeout <= 0 || *loginTimeout > 30*time.Minute || *sessionTimeout <= 0 || *sessionTimeout > 4*time.Hour || *sessionMode && len(command) > 0 || !*sessionMode && !validDeviceCommand(command) {
		return c.finish(videos.ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return c.finish(err)
	}
	if *sessionMode && c.input == nil {
		return c.finish(videos.ErrInput)
	}
	if !*sessionMode && !validDeviceSyntax(command) {
		return c.finish(videos.ErrInput)
	}
	// This fixed, explicitly configured binding is read only on an authorized
	// device-login invocation. Never discover credentials or retain it in children.
	secret := os.Getenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET")
	_ = os.Unsetenv("HHC_YOUTUBE_DEVICE_CLIENT_SECRET")
	if secret == "" {
		return c.finish(videos.FailureCode("device_client_not_configured"))
	}
	config := videoauth.DeviceConfig{ClientID: *clientID, ClientSecret: secret, ChannelID: *channel, Access: *access}
	login := loginYouTubeDevice
	if c.deviceLogin != nil {
		login = c.deviceLogin
	}
	loginCtx, cancelLogin := context.WithTimeout(ctx, *loginTimeout)
	grant, err := login(loginCtx, config, func(p videoauth.DevicePresentation) {
		fmt.Fprintln(c.diagnostics, "Google device sign-in:", p.VerificationURL)
		fmt.Fprintln(c.diagnostics, "Code:", p.UserCode)
		fmt.Fprintln(c.diagnostics, "Channel:", *channel)
		if *access == "edit" {
			fmt.Fprintln(c.diagnostics, "Consent scope: https://www.googleapis.com/auth/youtube (broad Google permission; CLI edits only title, description and tags).")
		} else {
			fmt.Fprintln(c.diagnostics, "Consent scope: https://www.googleapis.com/auth/youtube.readonly")
		}
		fmt.Fprintln(c.diagnostics, "Expires:", p.ExpiresAt.UTC().Format(time.RFC3339))
	})
	cancelLogin()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = videos.FailureCode("device_authorization_expired")
		}
		return c.finish(err)
	}
	expectedScope := youtube.ReadScope
	if *access == "edit" {
		expectedScope = youtube.ManageScope
	}
	if grant.ChannelID != *channel || grant.ClientID != *clientID || grant.Scope != expectedScope {
		return c.finish(videos.FailureCode("permission_denied"))
	}
	owner, err := videoauth.NewMemorySession(grant)
	if err != nil {
		return c.finish(err)
	}
	defer owner.Close()
	refresh := videoauth.NewGoogleRefresh().Refresh
	if c.videoRefresh != nil {
		refresh = c.videoRefresh
	}
	credential := func(ctx context.Context) (videoauth.Credentials, error) {
		v, e := owner.Credential(ctx, refresh)
		if e != nil {
			return videoauth.Credentials{}, e
		}
		return videoauth.Credentials{YouTube: &v}, nil
	}
	if !*sessionMode {
		exit, _ := deviceCommand(ctx, command, c, credential, false)
		return exit
	}
	// Explicit session mode emits NDJSON: one login result, then one schema-v1
	// result per command. stdout never includes device/access/refresh credentials.
	c.jsonMode = true
	c.requestedJSON = true
	c.result.Data = struct {
		Authenticated bool      `json:"authenticated"`
		ChannelID     string    `json:"channelId"`
		Scope         string    `json:"scope"`
		ExpiresAt     time.Time `json:"expiresAt"`
		Persistence   string    `json:"persistence"`
	}{true, grant.ChannelID, grant.Scope, grant.ExpiresAt, "process_only"}
	if exit := c.finish(nil); exit != 0 {
		return exit
	}
	sessionCtx, cancelSession := context.WithTimeout(ctx, *sessionTimeout)
	defer cancelSession()
	stopRead := context.AfterFunc(sessionCtx, func() { _ = c.input.Close() })
	defer stopRead()
	scanner := bufio.NewScanner(c.input)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	writesBlocked := false
	for count := 0; count < 100 && scanner.Scan(); count++ {
		if err := sessionCtx.Err(); err != nil {
			return deviceSessionError(c, err)
		}
		argv, decodeErr := decodeDeviceArgv(scanner.Bytes())
		if decodeErr != nil {
			deviceSessionError(c, videos.ErrInput)
			continue
		}
		if len(argv) == 1 && argv[0] == "exit" {
			return 0
		}
		exit, block := deviceCommand(sessionCtx, argv, c, credential, writesBlocked)
		writesBlocked = writesBlocked || block
		if exit == 3 || exit == 4 {
			return exit
		}
	}
	if err := sessionCtx.Err(); err != nil {
		return deviceSessionError(c, err)
	}
	if scanner.Err() != nil {
		return deviceSessionError(c, videos.ErrInput)
	}
	return 0
}

func validDeviceCommand(argv []string) bool {
	if len(argv) < 2 || len(argv) > 64 || argv[0] != "videos" {
		return false
	}
	switch argv[1] {
	case "search", "get", "compare", "edit":
	default:
		return false
	}
	for _, arg := range argv[2:] {
		if len(arg) > 8192 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) || strings.HasPrefix(arg, "--credentials-stdin") {
			return false
		}
	}
	return true
}
func deviceSessionError(c *commandContext, err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		err = videos.FailureCode("device_session_expired")
	}
	child := &commandContext{output: c.output, diagnostics: c.diagnostics, jsonMode: true, requestedJSON: true, result: result{SchemaVersion: 1, Command: "auth youtube-device"}}
	return child.finish(err)
}
func deviceCommand(ctx context.Context, argv []string, c *commandContext, credential videoCredential, writesBlocked bool) (int, bool) {
	if !validDeviceCommand(argv) {
		return deviceSessionError(c, videos.ErrInput), false
	}

	child := &commandContext{input: c.input, output: c.output, diagnostics: c.diagnostics, version: c.version, jsonMode: true, requestedJSON: true, result: result{SchemaVersion: 1}, videoCredential: credential, videoQuery: c.videoQuery, videoEdit: c.videoEdit}
	runEdit := editVideo
	if c.videoEdit != nil {
		runEdit = c.videoEdit
	}
	child.videoEdit = func(ctx context.Context, id string, patch youtube.Patch, apply bool, etag, hash string, credentials videoauth.Credentials) (youtube.EditResult, error) {
		if writesBlocked && apply {
			return youtube.EditResult{Outcome: "not_applied"}, videos.FailureCode("mutation_uncertain")
		}
		return runEdit(ctx, id, patch, apply, etag, hash, credentials)
	}
	args := append(append([]string(nil), argv[1:]...), "--json", "--no-input")
	exit := runVideos(ctx, args, child)
	block := child.result.Error != nil && (child.result.Error.Code == "mutation_uncertain" || child.result.Error.Code == "verification_failed")
	if result, ok := child.result.Data.(videos.Result); ok {
		for _, source := range result.Sources {
			if source.Source == "youtube" && source.Error != nil && (source.Error.HTTPStatus == 401 || source.Error.HTTPStatus == 403) {
				exit = 4
			}
		}
	}
	return exit, block
}

func validDeviceSyntax(argv []string) bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	child := &commandContext{output: io.Discard, diagnostics: io.Discard, jsonMode: true, requestedJSON: true, result: result{SchemaVersion: 1}, videoCredential: func(ctx context.Context) (videoauth.Credentials, error) { return videoauth.Credentials{}, ctx.Err() }}
	args := append(append([]string(nil), argv[1:]...), "--json", "--no-input")
	return runVideos(ctx, args, child) == 130
}

func decodeDeviceArgv(raw []byte) ([]string, error) {
	if !utf8.Valid(raw) {
		return nil, videos.ErrInput
	}
	var fields []json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 || len(fields) > 64 {
		return nil, videos.ErrInput
	}
	argv := make([]string, len(fields))
	for i, field := range fields {
		if len(field) == 0 || field[0] != '"' || json.Unmarshal(field, &argv[i]) != nil {
			return nil, videos.ErrInput
		}
	}
	return argv, nil
}
