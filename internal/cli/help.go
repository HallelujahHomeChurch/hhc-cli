package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

func printRootHelp(output io.Writer) {
	fmt.Fprintln(output, "HHC CLI — 家教會軟體命令列工具\n\n指令")
	for _, group := range commandGroups {
		fmt.Fprintf(output, "  %-12s %s\n", group.name, group.summary)
	}
	fmt.Fprintln(output, "\n查看操作方式：hhc <指令> -h\n錄影範例：hhc recordings upload -h")
}

// Help is handled before authentication, bundle verification or operation
// creation. Only known command paths are accepted; user arguments are not echoed.
func showHelp(args []string, output io.Writer) bool {
	if len(args) == 0 || len(args) == 1 && slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		printRootHelp(output)
		return true
	}
	helpArgs := args
	if len(args) > 3 && args[0] == "recordings" && slices.Contains([]string{"upload", "prepare", "get", "resume", "publish"}, args[1]) {
		// Existing recording commands consume args[2] as a positional value.
		// A complete invocation may legally name its source '-h' or '--help'.
		helpArgs = args[3:]
	}
	requested := helpRequested(helpArgs)
	if args[0] == "help" {
		args, requested = args[1:], true
	}
	if len(args) == 0 {
		return false
	}
	key := args[0]
	isGroup := false
	for _, group := range commandGroups {
		if group.name == key {
			isGroup = group.subcommands
			break
		}
	}
	if isGroup {
		if len(args) > 1 && args[1] != "-h" && args[1] != "--help" {
			key += " " + args[1]
		} else if len(args) == 1 {
			requested = true
		}
	}
	body, ok := commandHelp[key]
	if !requested || !ok {
		return false
	}
	fmt.Fprintln(output, "hhc "+key+" — "+body)
	if strings.Contains(key, " ") || key == "install" || key == "update" || key == "version" {
		fmt.Fprintln(output, "\n自動化：--json 輸出結構化結果；--no-input 不進行互動。")
		if strings.HasPrefix(key, "recordings ") && key != "recordings get" {
			if key != "recordings resume" {
				fmt.Fprintln(output, "  --operation-id UUID  手動操作可省略；自動化必須指定")
			}
			fmt.Fprintln(output, "  --timeout DURATION   操作期限，預設 4h\n  上傳／發布自動化須明確指定 --profile NAME；續傳沿用原操作 ID。")
		}
	}
	return true
}

func helpRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" {
			return true
		}
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		// These existing flags consume their next argv element even if it starts
		// with '-'. Match flag.FlagSet semantics; titles/scopes remain literal data.
		if strings.HasPrefix(arg, "-") && !inline && slices.Contains([]string{"profile", "client-id", "scope", "title", "cover", "operation-id", "output", "timeout", "directory", "query", "source", "limit", "max-pages", "locale", "description", "tags-json", "if-match", "plan-hash", "channel-id", "access", "login-timeout", "session-timeout"}, name) {
			i++
		}
	}
	return false
}

var commandHelp = map[string]string{
	"videos": `影片查詢與限定編輯

  search   搜尋 HHC／YouTube 影片
  get      依來源 ID 讀取資料
  compare  依 YouTube ID 比對網站公開狀態
  edit     預覽／套用 YouTube 標題、說明、標籤

公開 HHC 不需登入；私有 YouTube／HHC 使用已授權執行器的私有憑證 pipe。
結果的 incomplete、complete、coverage 與 error 必須一併判讀。`,
	"videos search": `搜尋影片

範例：hhc videos search --query "聚會" --source hhc --json
  --query TEXT         標題／摘要關鍵字
  --source SOURCE      all、hhc 或 youtube
  --limit NUMBER       每個來源最多回傳筆數，1–100，預設 20
  --max-pages NUMBER   每個來源最多分頁，1–10，預設 3
  --locale LOCALE      zh-Hant、zh-Hans、en
  --timeout DURATION   最多 30s，預設 30s
  --credentials-stdin  從私有 pipe 讀取短效唯讀 OAuth grant；拒絕一般檔案

HHC 公開清單在分頁上限內本機過濾。部分來源失敗以 incomplete=true 回報。`,
	"videos get": `讀取影片

範例：hhc videos get youtube:abcdefghijk --json
  ID                   hhc:ID 或 youtube:VIDEO_ID
  --limit／--max-pages／--locale／--timeout 同 search

公開 HHC ID 受分頁上限限制；已授權 human grant 可直接讀取。`,
	"videos compare": `比對網站公開狀態

範例：hhc videos compare abcdefghijk --json
  VIDEO_ID             11 字元 YouTube ID
  --limit／--max-pages／--locale／--timeout 同 search

僅用精確 youtubeVideoId；not_observed 不代表草稿、刪除或不存在。`,

	"videos edit": `限定編輯 YouTube 中繼資料

範例：hhc videos edit youtube:abcdefghijk --title "新標題" --dry-run --credentials-stdin --json
  --title TEXT         只修改標題
  --description TEXT   只修改說明；空字串明確清除
  --tags-json JSON     JSON 字串陣列；[] 明確清除
  --dry-run            讀取並預覽差異，預設行為
  --apply              明確套用一次；需帶回預覽的兩個前提
  --if-match ETAG      預覽的 ETag
  --plan-hash HASH     預覽的 planHash
  --credentials-stdin  私有 pipe 的已授權 youtube.force-ssl／youtube 憑證
  --timeout DURATION   最多 30s，預設 30s

保留分類與其他 snippet 欄位，使用 If-Match，套用後獨立讀回。
不確定結果不可自動重送。沒有上傳、刪除或隱私／發布變更指令。`,

	"auth": `帳號操作

  login   登入（一般帳號開啟瀏覽器）
  status  查看登入狀態
  logout  登出
  youtube-device  無瀏覽器 Google 裝置登入與本次程序的影片操作

查看範例：hhc auth login -h`,
	"auth youtube-device": `Google 無瀏覽器裝置登入

範例：hhc auth youtube-device --client-id ID --channel-id UC... --access read --json -- videos search --source youtube --query 聚會
  --client-id ID          既有 TVs and Limited Input devices OAuth client
  --channel-id UC...       已核准的 YouTube 頻道
  --access read|edit       read 預設；edit 明確要求較廣的 youtube scope
  --session               登入後在同一程序接受影片 argv JSON，每行一個陣列
  --login-timeout DURATION 授權等待預設 15m，最多 30m
  --session-timeout DURATION session 預設 1h，最多 4h／100 個指令

執行器需安全注入固定 HHC_YOUTUBE_DEVICE_CLIENT_SECRET 直接變數。
網址與 user code 顯示於 stderr；device/access/refresh token 不輸出或保存。
一次性模式以 -- 後指定 videos 指令；session 用 ["exit"] 結束。
沒有 callback、瀏覽器自動化或 Mac 相依。程序結束後需重新登入。`,

	"auth login": `登入帳號

範例：hhc auth login --profile uploader

  --profile NAME       登入設定名稱，預設 default
  --scope SCOPES       空白分隔的權限；預設錄影 read、write、publish

服務身分：hhc auth login --service-principal --client-id ID --profile uploader
  --secret-stdin       從標準輸入讀取機密（自動化搭配 --no-input）
  請勿把機密放在指令參數。一般帳號使用 --json／--no-input 不會開啟瀏覽器。`,
	"auth status": `查看登入狀態

範例：hhc auth status --profile uploader

  --profile NAME       登入設定名稱，預設 default`,
	"auth logout": `登出並清除本機登入憑證

範例：hhc auth logout --profile uploader

  --profile NAME       登入設定名稱，預設 default
  服務身分登出不會撤銷組織中的服務憑證。`,
	"recordings": `錄影操作

  upload   轉檔並上傳，預設只建立草稿
  resume   繼續未完成的操作
  get      查看錄影狀態
  publish  發布錄影
  prepare  只轉檔，保留輸出、不上傳

查看範例：hhc recordings upload -h`,
	"recordings upload": `上傳錄影，預設只建立草稿

範例：hhc recordings upload "video.mp4" --prepare --title "聚會錄影" --profile uploader

  FILE／DIRECTORY      原始影片檔，或已準備好的 HLS 目錄
  --prepare            原始影片須加此參數，先轉檔再上傳
  --title TITLE        必填：錄影標題
  --cover PATH         選填：16:9 JPEG／PNG，最多 5 MiB；選用成功後才發布
  --profile NAME       登入設定名稱，預設 default
  --publish            上傳驗證完成後發布（需要 publish 權限）

原始影片不會修改；上傳 ready 後清除工具產生的 HLS。
Windows 只使用 NVIDIA NVENC，不會回退其他 GPU 或 CPU。
中斷後用 hhc recordings resume 操作ID --profile uploader。`,
	"recordings prepare": `只轉檔成 HLS，保留輸出、不需要登入

範例：hhc recordings prepare "video.mp4" --output "new-hls"

  FILE                 原始影片檔
  --output DIRECTORY   必填：尚不存在的輸出目錄

原始影片不會修改；此指令的 HLS 輸出不會自動刪除。
Windows 只使用 NVIDIA NVENC，不會回退其他 GPU 或 CPU。`,
	"recordings resume": `繼續未完成的操作，不建立另一份錄影

範例：hhc recordings resume 操作ID --profile uploader

  UUID                 必填：原操作 ID
  --profile NAME       上傳操作沿用原登入設定；本機轉檔不需要

使用同一台電腦、同一個 OS 使用者及原操作儲存目錄。
若原操作要求發布，續傳仍會執行原發布意圖。`,
	"recordings get": `查看錄影狀態

範例：hhc recordings get 錄影ID --profile uploader

  ID                   必填：錄影 ID（不是操作 ID）
  --profile NAME       登入設定名稱，預設 default`,
	"recordings publish": `發布已完成驗證的錄影

範例：hhc recordings publish 錄影ID --profile uploader

  ID                   必填：錄影 ID（不是操作 ID）
  --profile NAME       登入設定名稱，預設 default

需要 cms:recordings:publish 權限。`,
	"update": `更新受管理安裝

範例：hhc update

  --check              只檢查新版本，不安裝

請先結束其他 hhc 操作；不會強制中斷工作。
Portable 版本須先用 hhc install 建立受管理安裝。`,
	"install": `建立受管理安裝

範例：hhc install --directory "完整安裝路徑"

  --directory PATH     必填：完整路徑，目錄尚不存在、父目錄已存在

不需管理員權限，不會覆蓋目錄或自動修改 PATH。
安裝後把此目錄加入使用者 PATH，使用根目錄的 hhc。`,
	"version": `查看版本

範例：hhc version

  --self-check         同時驗證內附轉檔工具`,
}
