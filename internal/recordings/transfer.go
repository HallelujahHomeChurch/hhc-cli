package recordings

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
)

var (
	ErrInvalidUploadTarget = errors.New("invalid_upload_target")
	ErrUploadURLRejected   = errors.New("upload_url_rejected")
	ErrTransferUnavailable = errors.New("transfer_unavailable")
	ErrPackageChanged      = errors.New("package_changed")
	r2Host                 = regexp.MustCompile(`^[a-f0-9]{32}\.r2\.cloudflarestorage\.com$`)
	packageIDPattern       = regexp.MustCompile(`^[a-f0-9]{32}$`)
	objectPathPattern      = regexp.MustCompile(`^(master\.m3u8|(720p|1080p)/(index\.m3u8|init\.mp4|seg-[0-9]{6}\.m4s))$`)
)

// SignedObject is an ephemeral wire capability, never a journal/output value.
type SignedObject struct {
	Path    string      `json:"path"`
	URL     string      `json:"url"`
	Method  string      `json:"method"`
	Headers http.Header `json:"headers"`
}

func (SignedObject) String() string     { return "[redacted upload capability]" }
func (s SignedObject) GoString() string { return s.String() }
func (SignedObject) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted upload capability]"`), nil
}

type Uploader struct{ http *http.Client }

func NewUploader() *Uploader {
	return &Uploader{http: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Put sends exactly one bounded object, not Account credentials. It performs no
// retries or auth renewal; the orchestrator must query server state/re-sign.
// A successful PUT is transport acceptance only, never package readiness.
func (u *Uploader) Put(ctx context.Context, file *os.File, packageID string, object media.RecordingPackageObject, signed SignedObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := validateTarget(packageID, object, signed)
	if err != nil {
		return err
	}
	if file == nil {
		return ErrPackageChanged
	}
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != object.SizeBytes {
		return ErrPackageChanged
	}
	fingerprint, err := media.FingerprintSource(ctx, file)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrPackageChanged
	}
	if fingerprint.SizeBytes != object.SizeBytes || fingerprint.SHA256 != object.SHA256 {
		return ErrPackageChanged
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), io.NewSectionReader(file, 0, object.SizeBytes))
	if err != nil {
		return ErrInvalidUploadTarget
	}
	request.ContentLength = object.SizeBytes
	request.Header.Set("Content-Type", signed.Headers.Get("Content-Type"))
	response, err := u.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// net/url.Error embeds the entire signed URL. Never return it.
		return ErrTransferUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return ErrUploadURLRejected
	}
	if response.StatusCode != http.StatusOK {
		return ErrTransferUnavailable
	}
	after, err := file.Stat()
	if err != nil || after.Size() != fingerprint.SizeBytes || !after.ModTime().Equal(fingerprint.ModifiedAt) {
		return ErrPackageChanged
	}
	return nil
}

func validateTarget(packageID string, object media.RecordingPackageObject, signed SignedObject) (*url.URL, error) {
	if !packageIDPattern.MatchString(packageID) || !objectPathPattern.MatchString(object.Path) || object.Path != signed.Path || signed.Method != http.MethodPut || object.SizeBytes <= 0 || object.SizeBytes > media.RecordingObjectMaxBytes || len(signed.URL) > 8192 {
		return nil, ErrInvalidUploadTarget
	}
	u, err := url.Parse(signed.URL)
	if err != nil || u.Scheme != "https" || !r2Host.MatchString(u.Host) || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return nil, ErrInvalidUploadTarget
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] != "recordings/packages/"+packageID+"/staging/"+object.Path {
		return nil, ErrInvalidUploadTarget
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, ErrInvalidUploadTarget
	}
	for _, key := range []string{"X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature"} {
		if len(q[key]) != 1 {
			return nil, ErrInvalidUploadTarget
		}
	}
	issued, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	ttl, ttlErr := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || ttlErr != nil || ttl < 1 || ttl > 900 || issued.After(time.Now().Add(time.Minute)) || !time.Now().Before(issued.Add(time.Duration(ttl)*time.Second)) || q.Get("X-Amz-SignedHeaders") != "content-length;content-type;host" || len(q.Get("X-Amz-Signature")) != 64 {
		return nil, ErrInvalidUploadTarget
	}
	contentType := "video/mp4"
	if strings.HasSuffix(object.Path, ".m3u8") {
		contentType = "application/vnd.apple.mpegurl"
	}
	for key, values := range signed.Headers {
		if len(values) != 1 {
			return nil, ErrInvalidUploadTarget
		}
		switch key {
		case "Content-Type":
			if values[0] != contentType {
				return nil, ErrInvalidUploadTarget
			}
		case "Content-Length":
			if values[0] != strconv.FormatInt(object.SizeBytes, 10) {
				return nil, ErrInvalidUploadTarget
			}
		case "Host":
			if values[0] != u.Host {
				return nil, ErrInvalidUploadTarget
			}
		default:
			return nil, ErrInvalidUploadTarget
		}
	}
	if signed.Headers.Get("Content-Type") != contentType || signed.Headers.Get("Content-Length") != strconv.FormatInt(object.SizeBytes, 10) {
		return nil, ErrInvalidUploadTarget
	}
	return u, nil
}
