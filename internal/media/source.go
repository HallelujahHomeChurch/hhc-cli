package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

type SourceFingerprint struct {
	SHA256     string    `json:"sha256"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// FingerprintSource hashes the stable source/snapshot, not encoded output.
// ReadAt leaves the handle's offset unchanged; memory is bounded to 64 KiB.
func FingerprintSource(ctx context.Context, file *os.File) (SourceFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return SourceFingerprint{}, err
	}
	if file == nil {
		return SourceFingerprint{}, ErrInvalidInput
	}
	before, err := file.Stat()
	if err != nil {
		return SourceFingerprint{}, err
	}
	if !before.Mode().IsRegular() {
		return SourceFingerprint{}, ErrUnsupportedSource
	}
	if err := ValidateRecordingSourceSize(before.Size()); err != nil {
		return SourceFingerprint{}, err
	}
	reader := io.NewSectionReader(file, 0, before.Size())
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return SourceFingerprint{}, err
		}
		n, err := reader.Read(buffer)
		hash.Write(buffer[:n])
		size += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return SourceFingerprint{}, err
		}
	}
	after, err := file.Stat()
	if err != nil {
		return SourceFingerprint{}, err
	}
	if size != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return SourceFingerprint{}, operation.ErrSourceChanged
	}
	return SourceFingerprint{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size, ModifiedAt: before.ModTime().UTC()}, nil
}
