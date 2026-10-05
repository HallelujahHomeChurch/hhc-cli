package recordings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/api"
)

var ErrCoverInput = errors.New("cover_input_invalid: use a JPEG/PNG up to 5 MiB, 24 MP, 8192 pixels, cropped to 16:9")
var ErrCoverOrientation = errors.New("cover_normalization_required: normalize EXIF orientation before upload")
var ErrCoverFailed = errors.New("cover_processing_failed")

type CoverProcessingError struct{ Reason string }

func (e *CoverProcessingError) Error() string {
	if coverReasonPattern.MatchString(e.Reason) {
		return ErrCoverFailed.Error() + ": " + e.Reason
	}
	return ErrCoverFailed.Error()
}
func (e *CoverProcessingError) Unwrap() error { return ErrCoverFailed }

var coverReasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type CoverState struct {
	Attempt         int               `json:"attempt"`
	SHA256          string            `json:"sha256"`
	SizeBytes       int64             `json:"sizeBytes"`
	ContentType     string            `json:"contentType"`
	UploadID        string            `json:"uploadId,omitempty"`
	AttemptKey      string            `json:"attemptKey"`
	SelectionKey    string            `json:"selectionKey,omitempty"`
	ExpectedVersion int64             `json:"expectedVersion,omitempty"`
	Receipt         *api.CoverReceipt `json:"receipt,omitempty"`
	State           string            `json:"state"`
	Cleaned         bool              `json:"cleaned"`
}

func validateCover(data []byte) (string, error) {
	if len(data) == 0 || len(data) > 5<<20 {
		return "", ErrCoverInput
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 24000000 || config.Width*9 != config.Height*16 {
		return "", ErrCoverInput
	}
	if format == "jpeg" {
		for offset := 2; offset < len(data); {
			if data[offset] != 0xff {
				return "", ErrCoverInput
			}
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				return "", ErrCoverInput
			}
			marker := data[offset]
			offset++
			if marker == 0xda {
				break
			}
			if len(data)-offset < 2 {
				return "", ErrCoverInput
			}
			size := int(binary.BigEndian.Uint16(data[offset : offset+2]))
			if size < 2 || size > len(data)-offset {
				return "", ErrCoverInput
			}
			segment := data[offset+2 : offset+size]
			if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
				if !coverEXIFNormal(segment[6:]) {
					return "", ErrCoverOrientation
				}
			}
			offset += size
		}
	}
	if format == "png" {
		ended := false
		for offset := 8; offset < len(data); {
			if len(data)-offset < 12 {
				return "", ErrCoverInput
			}
			size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
			if size > uint64(len(data)-offset-12) {
				return "", ErrCoverInput
			}
			kind := string(data[offset+4 : offset+8])
			if kind == "eXIf" && !coverEXIFNormal(data[offset+8:offset+8+int(size)]) {
				return "", ErrCoverOrientation
			}
			if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
				return "", ErrCoverInput
			}
			offset += int(size) + 12
			if kind == "IEND" {
				ended = size == 0 && offset == len(data)
				break
			}
		}
		if !ended {
			return "", ErrCoverInput
		}
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", ErrCoverInput
	}
	return "image/" + format, nil
}

func coverEXIFNormal(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return false
	}
	if order.Uint16(data[2:4]) != 42 {
		return false
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset+2 > uint64(len(data)) {
		return false
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+count*12+4 > uint64(len(data)) {
		return false
	}
	for i := uint64(0); i < count; i++ {
		tag := data[offset+i*12 : offset+(i+1)*12]
		if order.Uint16(tag[:2]) == 0x112 && (order.Uint16(tag[2:4]) != 3 || order.Uint32(tag[4:8]) != 1 || order.Uint16(tag[8:10]) != 1) {
			return false
		}
	}
	return true
}

func readCover(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 5<<20 {
		return nil, ErrCoverInput
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrCoverInput
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrCoverInput
	}
	data, err := io.ReadAll(io.LimitReader(f, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		return nil, ErrCoverInput
	}
	return data, nil
}

func SnapshotCover(j *Journal) error {
	if j.state.Intent.CoverPath == "" || j.state.Cover != nil {
		return nil
	}
	if rel, err := filepath.Rel(j.directory, j.state.Intent.CoverPath); err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrCoverInput
	}
	data, err := readCover(j.state.Intent.CoverPath)
	if err != nil {
		return err
	}
	contentType, err := validateCover(data)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	state := j.State()
	state.Cover = &CoverState{Attempt: 1, SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(data)), ContentType: contentType, AttemptKey: state.OperationID + ":cover:1", State: "pending"}
	if err := j.Save(state); err != nil {
		return err
	}
	return writeCover(j, data)
}

// Restore required local bytes before encoding/HLS mutation. Accepted remote
// uploads are reconciled first, so ready resume does not require the original.
func ensureCoverSnapshot(ctx context.Context, c *api.Client, j *Journal) error {
	if err := SnapshotCover(j); err != nil {
		return err
	}
	state := j.State()
	if state.Cover == nil || state.Cover.Receipt != nil {
		return nil
	}
	cover := *state.Cover
	state.Cover = &cover
	if cover.UploadID == "" && state.RecordingID != "" {
		list, err := retryControl(ctx, func() (api.CoverList, error) { return c.ListCovers(ctx, state.RecordingID) })
		if err != nil {
			return err
		}
		for _, item := range list.Items {
			if item.Kind == "custom" && item.OperationKey == cover.AttemptKey {
				cover.UploadID = item.UploadID
				cover.State = item.State
				if err := j.Save(state); err != nil {
					return err
				}
				break
			}
		}
	}
	if cover.UploadID != "" && cover.State != "uploading" && cover.State != "expired" {
		return nil
	}
	_, err := coverBytes(j)
	return err
}

func writeCover(j *Journal, data []byte) error {
	temp := ".cover.tmp"
	if err := j.root.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := j.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer j.root.Remove(temp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return j.root.Rename(temp, "cover.snapshot")
}

func coverBytes(j *Journal) ([]byte, error) {
	cover := j.state.Cover
	if cover == nil {
		return nil, ErrCoverInput
	}
	data, err := readCover(j.directory + string(os.PathSeparator) + "cover.snapshot")
	if err != nil {
		data, err = readCover(j.state.Intent.CoverPath)
		if err != nil {
			return nil, ErrCoverInput
		}
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != cover.SHA256 || int64(len(data)) != cover.SizeBytes {
		return nil, ErrCoverInput
	}
	if _, err := j.root.Lstat("cover.snapshot"); errors.Is(err, os.ErrNotExist) {
		if err := writeCover(j, data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (j *Journal) cleanCover() error {
	if j.state.Cover == nil {
		return nil
	}
	if err := j.root.Remove(".cover.tmp"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := j.root.Remove("cover.snapshot")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := j.State()
	next := *state.Cover
	next.Cleaned = true
	state.Cover = &next
	return j.Save(state)
}

func CompleteCover(ctx context.Context, c *api.Client, j *Journal) error {
	if j.state.Intent.CoverPath == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	state := j.State()
	if state.Cover == nil {
		if err := SnapshotCover(j); err != nil {
			return err
		}
		state = j.State()
	}
	cover := *state.Cover
	state.Cover = &cover
	for delay := time.Second; ; delay = min(30*time.Second, delay*2) {
		list, err := retryControl(ctx, func() (api.CoverList, error) { return c.ListCovers(ctx, state.RecordingID) })
		if err != nil {
			return err
		}
		if cover.Receipt != nil {
			if list.SelectedCoverID != cover.Receipt.CoverID {
				cover.State = "conflict"
				_ = j.Save(state)
				return ErrOperationConflict
			}
			cover.State = "selected"
			if err := j.Save(state); err != nil {
				return err
			}
			return j.cleanCover()
		}
		if cover.SelectionKey != "" {
			selected, err := retryControl(ctx, func() (api.CoverSelection, error) {
				return c.SelectCover(ctx, state.RecordingID, cover.UploadID, cover.ExpectedVersion, cover.SelectionKey)
			})
			if err != nil {
				return err
			}
			cover.Receipt = &selected.Receipt
			if selected.Outcome != "selected" {
				cover.State = "conflict"
				_ = j.Save(state)
				return ErrOperationConflict
			}
			cover.State = "selected"
			if err := j.Save(state); err != nil {
				return err
			}
			return j.cleanCover()
		}
		if cover.UploadID == "" {
			if cover.ExpectedVersion == 0 {
				cover.ExpectedVersion = list.RecordingVersion
				if err := j.Save(state); err != nil {
					return err
				}
			}
			for _, item := range list.Items {
				if item.Kind == "custom" && item.OperationKey == cover.AttemptKey {
					cover.UploadID = item.UploadID
					if cover.UploadID == "" {
						return api.ErrInvalidResponse
					}
					if err := j.Save(state); err != nil {
						return err
					}
					break
				}
			}
		}
		if cover.UploadID == "" {
			data, err := coverBytes(j)
			if err != nil {
				return err
			}
			attempt := 0
			upload, err := retryControl(ctx, func() (api.CoverUpload, error) {
				if attempt > 0 {
					remote, err := c.ListCovers(ctx, state.RecordingID)
					if err != nil {
						return api.CoverUpload{}, err
					}
					for _, item := range remote.Items {
						if item.Kind == "custom" && item.OperationKey == cover.AttemptKey {
							return api.CoverUpload{UploadID: item.UploadID, State: item.State}, nil
						}
					}
				}
				attempt++
				return c.UploadCover(ctx, state.RecordingID, data, cover.ContentType, cover.AttemptKey)
			})
			if err != nil {
				return err
			}
			cover.UploadID = upload.UploadID
			cover.State = upload.State
			if err := j.Save(state); err != nil {
				return err
			}
			continue
		}
		found := false
		for _, item := range list.Items {
			if item.UploadID != cover.UploadID {
				continue
			}
			found = true
			cover.State = item.State
			switch item.State {
			case "uploading":
				data, err := coverBytes(j)
				if err != nil {
					return err
				}
				upload, err := retryControl(ctx, func() (api.CoverUpload, error) {
					return c.UploadCover(ctx, state.RecordingID, data, cover.ContentType, cover.AttemptKey)
				})
				if err != nil {
					return err
				}
				if upload.UploadID != cover.UploadID {
					return api.ErrInvalidResponse
				}
				cover.State = upload.State
			case "ready":
				cover.State = "processing"
				cover.SelectionKey = cover.AttemptKey + ":select"
				if cover.ExpectedVersion == 0 {
					cover.ExpectedVersion = list.RecordingVersion
				}
			case "expired":
				if item.ID != "" && list.SelectedCoverID == item.ID {
					cover.State = "conflict"
					_ = j.Save(state)
					return ErrOperationConflict
				}
				if _, err := coverBytes(j); err != nil {
					return err
				}
				cover.Attempt++
				cover.AttemptKey = state.OperationID + ":cover:" + strconv.Itoa(cover.Attempt)
				cover.UploadID = ""
				cover.State = "pending"
			case "failed":
				cover.State = "failed"
				_ = j.Save(state)
				return &CoverProcessingError{Reason: item.Error}
			case "pending", "processing":
			default:
				return api.ErrInvalidResponse
			}
		}
		if !found {
			return api.ErrInvalidResponse
		}
		if err := j.Save(state); err != nil {
			return err
		}
		if cover.SelectionKey != "" || cover.UploadID == "" {
			continue
		}
		if err := waitContext(ctx, delay); err != nil {
			return err
		}
	}
}
