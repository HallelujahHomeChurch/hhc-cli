package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
	"golang.org/x/term"
)

type result struct {
	SchemaVersion int             `json:"schemaVersion"`
	OK            bool            `json:"ok"`
	Command       string          `json:"command"`
	Profile       *string         `json:"profile"`
	Principal     *auth.Principal `json:"principal"`
	Data          any             `json:"data"`
	Error         *commandError   `json:"error"`
	OperationID   *string         `json:"operationId,omitempty"`
}

type commandError struct {
	Code              string  `json:"code"`
	Message           string  `json:"message"`
	Retryable         bool    `json:"retryable"`
	RequestID         *string `json:"requestId"`
	ResumeOperationID *string `json:"resumeOperationId,omitempty"`
}

// Run never prints parser errors, remote response bodies or credential values.
// All output is selected here; auth tokens never enter the output envelope.
func Run(ctx context.Context, args []string, input *os.File, output, diagnostics io.Writer, version string) int {
	jsonMode := slices.Contains(args, "--json") || slices.Contains(args, "--json=true")
	r := result{SchemaVersion: 1}
	finish := func(err error) int {
		exit := 0
		if err != nil {
			code, message, status, retryable := classify(err)
			r.Error = &commandError{Code: code, Message: message, Retryable: retryable, ResumeOperationID: r.OperationID}
			exit = status
		}
		r.OK = err == nil
		if jsonMode {
			if json.NewEncoder(output).Encode(r) != nil {
				return 1
			}
		} else if err != nil {
			fmt.Fprintln(diagnostics, r.Error.Message)
		} else if r.Command == "version" {
			fmt.Fprintln(output, "hhc", version, runtime.GOOS+"/"+runtime.GOARCH)
		} else {
			encoder := json.NewEncoder(output)
			encoder.SetIndent("", "  ")
			if encoder.Encode(r) != nil {
				return 1
			}
		}
		return exit
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help")) {
		fmt.Fprintln(output, "Usage: hhc version [--json]\n       hhc auth login|status|logout [--profile NAME] [--json] [--no-input]\n\nLogin: --service-principal --client-id ID [--secret-stdin]\n       --scope 'cms:recordings:read cms:recordings:write cms:recordings:publish'\n\nHuman login opens the system browser. Service secrets are hidden; never use a secret argument.")
		fmt.Fprintln(output, "\nRecording metadata: hhc recordings get ID [--profile NAME] [--json] [--no-input]")
		fmt.Fprintln(output, "Upload package: hhc recordings upload DIRECTORY --title TITLE --profile NAME --operation-id UUID [--publish] [--timeout 4h] [--json] [--no-input]\nResume: hhc recordings resume UUID --profile NAME [--timeout 4h] [--json] [--no-input]")
		fmt.Fprintln(output, "Publish: hhc recordings publish ID --profile NAME --operation-id UUID [--timeout 4h] [--json] [--no-input]")
		fmt.Fprintln(output, "Prepare and upload: hhc recordings upload FILE --prepare --title TITLE --profile NAME --operation-id UUID [--publish] [--json] [--no-input]")
		fmt.Fprintln(output, "Keep prepared package: hhc recordings prepare FILE --output DIRECTORY --operation-id UUID [--timeout 4h] [--json] [--no-input]")
		return 0
	}
	var flags []string
	var recordingID string
	var recordingInput, recordingOutput, operationID, title string
	var publish, prepare bool
	var explicitProfile, noninteractive bool
	timeout := 4 * time.Hour
	if args[0] == "version" {
		r.Command, flags = "version", args[1:]
	} else if len(args) >= 2 && args[0] == "auth" && slices.Contains([]string{"login", "status", "logout"}, args[1]) {
		r.Command, flags = "auth "+args[1], args[2:]
	} else if len(args) >= 3 && args[0] == "recordings" && args[1] == "get" {
		r.Command, recordingID, flags = "recordings get", args[2], args[3:]
		if !api.ValidRecordingID(recordingID) {
			return finish(auth.ErrInvalidAuthInput)
		}
	} else if len(args) >= 3 && args[0] == "recordings" && slices.Contains([]string{"upload", "resume", "publish", "prepare"}, args[1]) {
		r.Command, recordingInput, flags = "recordings "+args[1], args[2], args[3:]
		if args[1] == "resume" {
			operationID = recordingInput
		}
		if args[1] == "publish" {
			publish = true
			if !api.ValidRecordingID(recordingInput) {
				return finish(auth.ErrInvalidAuthInput)
			}
		}
	} else {
		return finish(auth.ErrInvalidAuthInput)
	}
	fs := flag.NewFlagSet("hhc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&jsonMode, "json", false, "")
	noInput := fs.Bool("no-input", false, "")
	profile := "default"
	var service, secretStdin bool
	var clientID, scope string
	if r.Command != "version" && r.Command != "recordings prepare" {
		fs.StringVar(&profile, "profile", "default", "")
	}
	if r.Command == "auth login" {
		fs.BoolVar(&service, "service-principal", false, "")
		fs.BoolVar(&secretStdin, "secret-stdin", false, "")
		fs.StringVar(&clientID, "client-id", "", "")
		fs.StringVar(&scope, "scope", "cms:recordings:read cms:recordings:write cms:recordings:publish", "")
	}
	if r.Command == "recordings upload" {
		fs.StringVar(&title, "title", "", "")
		fs.StringVar(&operationID, "operation-id", "", "")
		fs.BoolVar(&publish, "publish", false, "")
		fs.BoolVar(&prepare, "prepare", false, "")
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
	if err := fs.Parse(flags); err != nil || fs.NArg() != 0 {
		// Parse can stop before --json. Preserve the requested machine envelope.
		jsonMode = jsonMode || slices.Contains(args, "--json") || slices.Contains(args, "--json=true")
		return finish(auth.ErrInvalidAuthInput)
	}
	if r.Command == "version" {
		r.Data = struct {
			Version  string `json:"version"`
			Platform string `json:"platform"`
		}{version, runtime.GOOS + "/" + runtime.GOARCH}
		return finish(nil)
	}
	if r.Command == "recordings upload" || r.Command == "recordings resume" || r.Command == "recordings publish" || r.Command == "recordings prepare" {
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "profile" {
				explicitProfile = true
			}
		})
		noninteractive = *noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd()))
		needsProfile := r.Command == "recordings upload" || r.Command == "recordings publish"
		if timeout <= 0 || noninteractive && (needsProfile && !explicitProfile || operationID == "") || r.Command == "recordings upload" && strings.TrimSpace(title) == "" || r.Command == "recordings prepare" && recordingOutput == "" {
			return finish(auth.ErrInvalidAuthInput)
		}
		if operationID == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return finish(err)
			}
			id[6] = (id[6] & 15) | 64
			id[8] = (id[8] & 63) | 128
			operationID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		}
		if !api.ValidRecordingID(operationID) {
			return finish(auth.ErrInvalidAuthInput)
		}
		r.OperationID = &operationID
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return finish(err)
	}
	if !auth.ValidProfile(profile) {
		return finish(auth.ErrInvalidAuthInput)
	}
	r.Profile = &profile
	if r.Command == "auth login" && !service {
		if secretStdin || clientID != "" {
			return finish(auth.ErrInvalidAuthInput)
		}
		if *noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd())) {
			return finish(auth.ErrAuthenticationRequired)
		}
	}
	if r.Command == "auth login" && service && (clientID == "" || (!secretStdin && (*noInput || jsonMode || input == nil || !term.IsTerminal(int(input.Fd()))))) {
		return finish(auth.ErrInvalidAuthInput)
	}
	directory, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		directory, err = os.UserCacheDir()
	}
	if err != nil || !filepath.IsAbs(directory) {
		return finish(auth.ErrCredentialStoreUnavailable)
	}
	operations := filepath.Join(directory, "HHC", "cli", "operations")
	if configured := os.Getenv("HHC_CLI_OPERATIONS_DIR"); configured != "" {
		if !filepath.IsAbs(configured) {
			return finish(auth.ErrInvalidAuthInput)
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
				return finish(auth.ErrInvalidAuthInput)
			}
			options := media.DefaultEncodeOptions()
			intent = &recordings.Intent{Command: "prepare", Input: source, Output: output, VideoBitrate720: options.VideoBitrate720, VideoBitrate1080: options.VideoBitrate1080}
		}
		journal, err := recordings.OpenJournal(operations, operationID, intent)
		if err != nil {
			return finish(err)
		}
		if journal.State().Intent.Command == "prepare" {
			defer journal.Close()
			r.Profile = nil
			if !jsonMode {
				fmt.Fprintln(diagnostics, "Operation:", operationID)
			}
			r.Data, err = recordings.Prepare(ctx, journal)
			if err != nil {
				err = errors.Join(err, journal.Save(journal.State()))
			}
			return finish(err)
		}
		journal.Close()
		if noninteractive && !explicitProfile {
			return finish(auth.ErrInvalidAuthInput)
		}
	}
	profiles := auth.NewProfiles(filepath.Join(directory, "HHC", "cli", "profiles"))
	var token auth.Token
	switch r.Command {
	case "recordings upload", "recordings resume", "recordings publish":
		token, err = profiles.Token(ctx, profile)
		if err != nil {
			return finish(err)
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
			return finish(err)
		}
		var intent *recordings.Intent
		if r.Command == "recordings publish" {
			intent = &recordings.Intent{Command: "publish", Profile: profile, PrincipalType: principal.Type, PrincipalID: principal.ID, ClientID: principal.ClientID, RecordingID: recordingInput, Publish: true}
		}
		if r.Command == "recordings upload" {
			path, err := filepath.Abs(recordingInput)
			if err != nil {
				return finish(auth.ErrInvalidAuthInput)
			}
			intent = &recordings.Intent{Command: "upload", Profile: profile, PrincipalType: principal.Type, PrincipalID: principal.ID, ClientID: principal.ClientID, Input: path, Title: strings.TrimSpace(title), Publish: publish}
			if prepare {
				options := media.DefaultEncodeOptions()
				intent.Prepare, intent.VideoBitrate720, intent.VideoBitrate1080 = true, options.VideoBitrate720, options.VideoBitrate1080
			}
		}
		journal, err := recordings.OpenJournal(operations, operationID, intent)
		if err != nil {
			return finish(err)
		}
		defer journal.Close()
		if journal.State().Intent.Profile != profile {
			return finish(recordings.ErrOperationConflict)
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
		return finish(err)
	case "recordings get":
		token, err = profiles.Token(ctx, profile)
		if err != nil {
			return finish(err)
		}
		client := api.NewClient(token, func(ctx context.Context) (auth.Token, error) { return profiles.Token(ctx, profile) })
		recording, getErr := client.GetRecording(ctx, recordingID)
		err = getErr
		if err == nil {
			r.Data = recording
		}
		principal := client.Principal()
		r.Principal = &principal
		return finish(err)
	case "auth status":
		token, err = profiles.Token(ctx, profile)
	case "auth logout":
		r.Data, err = profiles.Logout(ctx, profile)
		return finish(err)
	case "auth login":
		if service {
			var secret []byte
			secret, err = readSecret(ctx, input, diagnostics, secretStdin)
			if err != nil {
				return finish(err)
			}
			defer clear(secret)
			token, err = profiles.LoginService(ctx, profile, clientID, string(secret), strings.Fields(scope))
		} else {
			token, err = profiles.LoginHuman(ctx, profile, auth.HumanLoginOptions{Scopes: strings.Fields(scope)})
		}
	}
	if err == nil {
		principal := token.Principal()
		r.Principal = &principal
		r.Data = struct {
			Scopes          []string  `json:"scopes"`
			AccessExpiresAt time.Time `json:"accessExpiresAt"`
		}{strings.Fields(token.Scope()), token.ExpiresAt()}
	}
	return finish(err)
}

func readSecretLine(reader io.Reader) ([]byte, error) {
	line, err := bufio.NewReaderSize(reader, 4098).ReadSlice('\n')
	if err != nil && err != io.EOF {
		clear(line)
		return nil, auth.ErrInvalidAuthInput
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 || len(line) > 4096 || !utf8.Valid(line) || bytes.ContainsAny(line, "\r\n\x00") {
		clear(line)
		return nil, auth.ErrInvalidAuthInput
	}
	return line, nil
}

func readSecret(ctx context.Context, input *os.File, diagnostics io.Writer, fromStdin bool) ([]byte, error) {
	if input == nil {
		return nil, auth.ErrInvalidAuthInput
	}
	fd := int(input.Fd())
	if fromStdin && term.IsTerminal(fd) {
		return nil, auth.ErrInvalidAuthInput
	}
	if !fromStdin {
		state, err := term.GetState(fd)
		if err != nil {
			return nil, auth.ErrInvalidAuthInput
		}
		defer term.Restore(fd, state)
		fmt.Fprint(diagnostics, "Service credential (hidden): ")
		defer fmt.Fprintln(diagnostics)
	}
	type secretResult struct {
		value []byte
		err   error
	}
	completed := make(chan secretResult)
	go func() {
		var value []byte
		var err error
		if fromStdin {
			value, err = readSecretLine(input)
		} else {
			value, err = term.ReadPassword(fd)
		}
		select {
		case completed <- secretResult{value, err}:
		case <-ctx.Done():
			clear(value)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-completed:
		if result.err != nil {
			clear(result.value)
			return nil, auth.ErrInvalidAuthInput
		}
		return result.value, nil
	}
}

func classify(err error) (string, string, int, bool) {
	var remote *api.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case "api_unavailable", "rate_limited":
			return remote.Code, "錄影服務暫時無法確認結果，請稍後重試。", 6, true
		case "not_found", "operation_conflict", "state_changed":
			return remote.Code, "錄影不存在或狀態已變更，請重新查詢。", 5, false
		}
	}
	switch {
	case errors.Is(err, media.ErrLocalCleanup):
		return "local_cleanup_failed", "暫存清理未完成，請以原 operation ID 執行 resume。", 6, true
	case errors.Is(err, bundle.ErrUnavailable):
		return "ffmpeg_bundle_unavailable", "內附轉檔工具缺少或驗證失敗，請重新安裝可信任的 HHC 發行包。", 5, false
	case errors.Is(err, media.ErrInsufficientDisk):
		return "insufficient_disk_space", "磁碟可用空間不足；清出空間後以原 operation ID 執行 resume。", 5, true
	case errors.Is(err, media.ErrRecordingSourceTooLarge):
		return "source_too_large", "來源超過 50 GB 上限。", 2, false
	case errors.Is(err, media.ErrRecordingPackageTooLarge), errors.Is(err, media.ErrRecordingPackageEstimateTooLarge):
		return "package_too_large", "HLS 套件超過 10 GB 上限或估算預算，未完成上傳。", 2, false
	case errors.Is(err, media.ErrUnsupportedSource):
		return "unsupported_source", "來源格式、影音軌或畫面規格不支援。", 2, false
	case errors.Is(err, operation.ErrSourceChanged):
		return "source_changed", "來源檔案已變動，未繼續轉檔或上傳。", 5, false
	case errors.Is(err, api.ErrInvalidResponse):
		return "invalid_api_response", "錄影回應不符合可信契約。", 6, false
	case errors.Is(err, context.Canceled):
		return "cancelled", "操作已取消。", 130, false
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "操作逾時，尚未確認完成。", 6, true
	case errors.Is(err, auth.ErrInvalidAuthInput):
		return "invalid_input", "參數無效；請使用 hhc --help。", 2, false
	case errors.Is(err, media.ErrInvalidInput), errors.Is(err, recordings.ErrInvalidJournal):
		return "invalid_input", "套件或操作紀錄無效，未確認完成。", 2, false
	case errors.Is(err, recordings.ErrOperationConflict), errors.Is(err, recordings.ErrPackageChanged):
		return "operation_conflict", "操作身分、內容或狀態已變更，請檢查原操作。", 5, false
	case errors.Is(err, recordings.ErrSessionExpired):
		return "session_expired", "上傳期限已過，不會自動建立另一份錄影。", 5, false
	case errors.Is(err, recordings.ErrPackageFailed):
		return "package_failed", "伺服器影片驗證失敗，未發布。", 5, false
	case errors.Is(err, recordings.ErrTransferUnavailable), errors.Is(err, recordings.ErrUploadURLRejected):
		return "transfer_unavailable", "上傳未完成，請以原 operation ID 執行 resume。", 6, true
	case errors.Is(err, auth.ErrAuthenticationRequired):
		return "authentication_required", "需要執行 hhc auth login 登入。", 3, false
	case errors.Is(err, auth.ErrCredentialStoreUnavailable):
		return "credential_store_unavailable", "無法存取作業系統憑證庫。", 3, false
	case errors.Is(err, auth.ErrPermissionDenied):
		return "permission_denied", "此身分沒有所需權限。", 4, false
	case errors.Is(err, operation.ErrOperationBusy):
		return "operation_busy", "相同 profile 正在使用中。", 5, true
	case errors.Is(err, auth.ErrAuthUnavailable):
		return "auth_unavailable", "驗證服务暫時無法確認結果。", 6, true
	case errors.Is(err, auth.ErrInvalidAuthResponse):
		return "invalid_auth_response", "驗證回應不符合可信契約。", 6, false
	default:
		return "unknown_error", "操作失敗，尚未確認完成。", 1, false
	}
}
