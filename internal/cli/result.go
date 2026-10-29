package cli

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/bundle"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/recordings"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/update"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
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
	Stage             string  `json:"stage,omitempty"`
	HTTPStatus        int     `json:"httpStatus,omitempty"`
	ServerCode        string  `json:"serverCode,omitempty"`
	RequestID         *string `json:"requestId"`
	ResumeOperationID *string `json:"resumeOperationId,omitempty"`
}

func classify(err error) (string, string, int, bool) {
	// Preserve existing recovery codes and expose only fixed stages/numeric OS
	// codes. Never print wrapped errors: they may contain local paths or URLs.
	code, message, exit, retryable := classifyCause(err)
	var validation *media.ValidationFailure
	diagnostic := ""
	if errors.As(err, &validation) {
		diagnostic = " HLS 驗證：" + validation.Error() + "。請保留原 operation ID 與此診斷訊息。"
		if code == "invalid_input" {
			message = "本機 HLS 產物驗證失敗，未確認完成。"
		}
	}
	var queryFailure *videos.Failure
	if errors.As(err, &queryFailure) {
		switch queryFailure.Code {
		case "device_client_not_configured":
			return "device_client_not_configured", "請先由執行器安全注入既有 Google device client secret。", 3, false
		case "device_authorization_denied":
			return "device_authorization_denied", "Google 裝置授權已拒絕，未登入。", 3, false
		case "device_authorization_expired", "device_session_expired":
			return queryFailure.Code, "本次裝置授權或程序 session 已到期；未保存 token。", 3, false
		case "device_authorization_uncertain", "device_authorization_unavailable", "device_poll_timing", "device_session_used", "session_refresh_uncertain":
			return queryFailure.Code, "本次 Google 授權狀態無法安全確認；不會重播代碼或憑證。", 6, false
		case "authentication_required":
			return "authentication_required", "需要執行器提供有效的唯讀授權。", 3, false
		case "state_changed":
			return "state_changed", "影片內容或預覽已變更，請重新讀取並檢查差異。", 5, false
		case "mutation_uncertain", "verification_failed":
			return queryFailure.Code, "寫入結果尚未確認；請唯讀查詢比對，不要自動重送。", 6, false
		case "invalid_metadata", "unsupported_metadata":
			return queryFailure.Code, "影片資料無法安全更新，未確認套用。", 5, false
		case "rate_limited":
			return "rate_limited", "YouTube 配額或速率限制；本次不自動重送編輯。", 6, false
		case "not_found":
			return "not_found", "找不到已授權頻道的指定影片。", 5, false
		case "permission_or_quota_denied":
			return "permission_or_quota_denied", "YouTube 權限或配額不足；請由授權擁有者確認。", 4, false
		case "resource_denied":
			return "resource_denied", "指定影片不在已授權的頻道範圍。", 4, false
		case "permission_denied":
			return "permission_denied", "提供的授權不符合來源或唯讀權限。", 4, false
		}
	}
	var remote *api.Error
	if errors.As(err, &remote) {
		if remote.Status > 0 {
			diagnostic += fmt.Sprintf("（HTTP %d / %s）", remote.Status, code)
		}
		if label := recordingStageLabel(remote.Stage); label != "" {
			message = "階段 " + label + "（" + remote.Stage + "）：" + message
		}
	}
	var preparation *media.PreparationFailure
	if errors.As(err, &preparation) {
		stage := preparation.Stage
		if validation != nil {
			stage = "hls_validate"
		}
		if stage == "encode_nvenc" && code == "media_process_failed" {
			message += " NVIDIA NVENC 轉檔失敗；請確認顯卡支援、驅動與來源檔案。不會改用其他 GPU 或 CPU；排除原因後以原操作 ID 執行 hhc recordings resume。"
		}
		if code == "unknown_error" {
			var systemCode syscall.Errno
			errors.As(preparation.Cause, &systemCode)
			code, message = "preparation_failed", fmt.Sprintf("本機準備失敗（os=%d），請保留原 operation ID。", uint64(systemCode))
		}
		return code, "階段 " + stage + "：" + message + diagnostic, exit, retryable
	}
	return code, message + diagnostic, exit, retryable
}

func classifyCause(err error) (string, string, int, bool) {
	var cleanup *update.CleanupFailure
	if errors.As(err, &cleanup) {
		return "update_cleanup_failed", fmt.Sprintf("安裝／更新暫存清理未完成（os=%d）；請查看 data.installed，勿重跑 install 覆蓋目錄。", cleanup.SystemCode), 1, false
	}
	var process *operation.ProcessFailure
	if errors.As(err, &process) && !errors.Is(err, media.ErrLocalCleanup) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return "media_process_failed", fmt.Sprintf("媒體工具失敗（stage=%s exit=%d os=%d），請保留原 operation ID 供診斷。", process.Stage, process.ExitCode, process.SystemCode), 1, false
	}
	var remote *api.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case "api_unavailable", "rate_limited":
			return remote.Code, "錄影服務暫時無法確認結果，請稍後重試。", 6, true
		case "not_found":
			return remote.Code, "找不到錄影或上傳資源；可能已刪除或到期。請確認錄影狀態，勿直接重建上傳。", 5, false
		case "operation_conflict":
			if remote.ServerCode == "recording_draft_limit" && remote.Stage == "recording_create" {
				return remote.Code, "未完成上傳的錄影已達上限（2 筆，recording_draft_limit）。請先完成其他上傳；排除後以原 operation 執行 hhc recordings resume。", 5, false
			}
			return remote.Code, "操作衝突，平台未接受本次變更。請確認現有錄影與操作狀態，保留原 operation，勿重新 upload。", 5, false
		case "state_changed":
			if remote.Status == 428 {
				return remote.Code, "缺少版本前提，平台未接受本次變更。請確認 CLI 版本並保留原 operation。", 5, false
			}
			return remote.Code, "錄影版本已變更，平台未接受本次變更。請重新查詢，勿覆蓋其他人的修改。", 5, false
		}
	}
	switch {
	case errors.Is(err, update.ErrManagedInstallRequired):
		return "managed_install_required", "此為 portable 安裝，未更新；請依發行包說明建立使用者層級受管理安裝。", 5, false
	case errors.Is(err, update.ErrTrustUnavailable):
		return "release_trust_unavailable", "此版本未內附發行驗證金鑰；不會下載或執行未驗證的更新。", 5, false
	case errors.Is(err, update.ErrInvalidRelease):
		return "invalid_release", "更新套件或相容性驗證失敗，未確認安裝成功。", 5, false
	case errors.Is(err, update.ErrReleaseUnavailable):
		return "release_unavailable", "暫時無法取得可信發行資訊。", 6, true
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
	case errors.Is(err, videos.ErrInput), errors.Is(err, auth.ErrInvalidAuthInput):
		return "invalid_input", "參數無效；請使用 hhc --help。", 2, false
	case errors.Is(err, media.ErrInvalidInput), errors.Is(err, recordings.ErrInvalidJournal):
		return "invalid_input", "套件或操作紀錄無效，未確認完成。", 2, false
	case errors.Is(err, recordings.ErrCoverInput):
		return "cover_input_invalid", recordings.ErrCoverInput.Error(), 2, false
	case errors.Is(err, recordings.ErrCoverOrientation):
		return "cover_normalization_required", recordings.ErrCoverOrientation.Error(), 2, false
	case errors.Is(err, recordings.ErrCoverFailed):
		var coverFailure *recordings.CoverProcessingError
		if errors.As(err, &coverFailure) {
			return "cover_processing_failed", coverFailure.Error(), 5, false
		}
		return "cover_processing_failed", "封面處理失敗，影片未發布。", 5, false
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
		return "operation_busy", "相同安裝環境、profile 或操作正在使用中。", 5, true
	case errors.Is(err, auth.ErrAuthUnavailable):
		return "auth_unavailable", "驗證服务暫時無法確認結果。", 6, true
	case errors.Is(err, auth.ErrInvalidAuthResponse):
		return "invalid_auth_response", "驗證回應不符合可信契約。", 6, false
	default:
		return "unknown_error", "操作失敗，尚未確認完成。", 1, false
	}
}

func recordingStageLabel(stage string) string {
	switch stage {
	case "recording_create":
		return "建立錄影"
	case "recording_get":
		return "查詢錄影"
	case "package_create":
		return "建立上傳套件"
	case "package_status":
		return "查詢上傳與驗證進度"
	case "package_sign":
		return "取得上傳授權"
	case "package_complete":
		return "提交上傳完成"
	case "recording_publish":
		return "發布錄影"
	case "cover_list":
		return "查詢縮圖"
	case "cover_upload":
		return "上傳縮圖"
	case "cover_select":
		return "選取縮圖"
	default:
		return ""
	}
}
