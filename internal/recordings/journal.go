package recordings

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/auth"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

var (
	ErrOperationConflict = errors.New("operation_conflict")
	ErrInvalidJournal    = errors.New("invalid_journal")
	ErrJournalSchema     = errors.New("unsupported_journal_schema")
	uuidPattern          = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

// Intent is fixed before the first remote mutation. It contains no credentials,
// signed capabilities or server-derived permission claims.
type Intent struct {
	Command          string `json:"command"`
	Profile          string `json:"profile"`
	PrincipalType    string `json:"principalType"`
	PrincipalID      string `json:"principalId"`
	ClientID         string `json:"clientId"`
	Input            string `json:"input"`
	Output           string `json:"output"`
	Title            string `json:"title"`
	Prepare          bool   `json:"prepare"`
	Publish          bool   `json:"publish"`
	RecordingID      string `json:"recordingId"`
	VideoBitrate720  int64  `json:"videoBitrate720"`
	VideoBitrate1080 int64  `json:"videoBitrate1080"`
}

type JournalState struct {
	SchemaVersion          int                     `json:"schemaVersion"`
	OperationID            string                  `json:"operationId"`
	Intent                 Intent                  `json:"intent"`
	UpdatedAt              time.Time               `json:"updatedAt"`
	SourceFingerprint      media.SourceFingerprint `json:"sourceFingerprint"`
	PackageDigest          string                  `json:"packageDigest"`
	RecordingID            string                  `json:"recordingId"`
	PackageID              string                  `json:"packageId"`
	SessionID              string                  `json:"sessionId"`
	PublishExpectedVersion int64                   `json:"publishExpectedVersion"`
	GeneratedOwned         bool                    `json:"generatedOwned"`
	PackageBytes           int64                   `json:"packageBytes"`
	LastResult             *UploadResult           `json:"lastResult,omitempty"`
}

// Journal is held by one command. Its OS lock excludes other processes; the
// orchestrator serializes saves. Remote IDs are hints: resume queries the server
// again and never treats this local file as proof of readiness or publication.
type Journal struct {
	root      *os.Root
	lock      *os.File
	directory string
	state     JournalState
}

func OpenJournal(base, id string, intent *Intent) (*Journal, error) {
	if !filepath.IsAbs(base) || !validUUID(id) || intent != nil && !validIntent(*intent) {
		return nil, ErrInvalidJournal
	}
	directory := filepath.Join(base, id)
	if intent == nil {
		if _, err := os.Lstat(directory); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	baseInfo, err := os.Lstat(base)
	if err != nil || !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidJournal
	}
	if intent != nil {
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	lock, err := operation.LockWorkspace(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		lock.Close()
		return nil, err
	}
	j := &Journal{root: root, lock: lock, directory: directory}
	fail := func(err error) (*Journal, error) { j.Close(); return nil, err }
	info, err := root.Lstat("journal.json")
	if errors.Is(err, os.ErrNotExist) {
		if intent == nil {
			return fail(err)
		}
		j.state = JournalState{SchemaVersion: 1, OperationID: id, Intent: *intent}
		if err := j.Save(j.state); err != nil {
			return fail(err)
		}
		return j, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return fail(ErrInvalidJournal)
	}
	f, err := root.Open("journal.json")
	if err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	f.Close()
	if err != nil || len(data) > 65536 || !utf8.Valid(data) {
		return fail(ErrInvalidJournal)
	}
	var version struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if json.Unmarshal(data, &version) != nil {
		return fail(ErrInvalidJournal)
	}
	if version.SchemaVersion != 1 {
		return fail(ErrJournalSchema)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&j.state) != nil || !validState(j.state) || j.state.OperationID != id {
		return fail(ErrInvalidJournal)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fail(ErrInvalidJournal)
	}
	if intent != nil && *intent != j.state.Intent {
		return fail(ErrOperationConflict)
	}
	return j, nil
}

func (j *Journal) State() JournalState { return j.state }

func (j *Journal) Save(next JournalState) error {
	if j.lock == nil || !validState(next) {
		return ErrInvalidJournal
	}
	if next.OperationID != j.state.OperationID || next.Intent != j.state.Intent ||
		j.state.RecordingID != "" && next.RecordingID != j.state.RecordingID ||
		j.state.PackageID != "" && next.PackageID != j.state.PackageID ||
		j.state.SessionID != "" && next.SessionID != j.state.SessionID ||
		j.state.PublishExpectedVersion != 0 && next.PublishExpectedVersion != j.state.PublishExpectedVersion ||
		j.state.SourceFingerprint.SHA256 != "" && next.SourceFingerprint != j.state.SourceFingerprint ||
		j.state.PackageDigest != "" && next.PackageDigest != j.state.PackageDigest {
		return ErrOperationConflict
	}
	if j.state.GeneratedOwned && !next.GeneratedOwned || j.state.PackageBytes != 0 && next.PackageBytes != j.state.PackageBytes {
		return ErrOperationConflict
	}
	next.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(next)
	if err != nil || len(data) > 65536 {
		return ErrInvalidJournal
	}
	temp := ".journal-" + rand.Text() + ".tmp"
	f, err := j.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer j.root.Remove(temp)
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if info, err := j.root.Lstat("journal.json"); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidJournal
	}
	if err := replaceJournal(j.root, j.directory, temp); err != nil {
		return err
	}
	j.state = next
	return nil
}

func (j *Journal) Close() error {
	if j.lock == nil {
		return nil
	}
	err := errors.Join(j.root.Close(), j.lock.Close())
	j.lock = nil
	return err
}

func validUUID(id string) bool {
	return uuidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func validIntent(v Intent) bool {
	if v.Command != "prepare" && v.Command != "upload" && v.Command != "publish" {
		return false
	}
	if v.Command != "prepare" && (!auth.ValidProfile(v.Profile) || !validUUID(v.PrincipalID) || v.PrincipalType != "human" && v.PrincipalType != "service" || v.ClientID == "" || len(v.ClientID) > 128) {
		return false
	}
	if v.Command == "prepare" && (v.Profile != "" || v.PrincipalID != "" || v.PrincipalType != "" || v.ClientID != "" || v.Publish || !filepath.IsAbs(v.Output)) {
		return false
	}
	if v.Command == "publish" {
		return validUUID(v.RecordingID) && v.Input == "" && v.Output == "" && !v.Prepare && v.Publish
	}
	if !filepath.IsAbs(v.Input) || strings.ContainsRune(v.Input+v.Output, 0) || len(v.Input) > 4096 || len(v.Output) > 4096 {
		return false
	}
	if v.Command == "upload" && (strings.TrimSpace(v.Title) == "" || !utf8.ValidString(v.Title) || utf8.RuneCountInString(v.Title) > 180) {
		return false
	}
	if v.Command == "prepare" || v.Prepare {
		return v.VideoBitrate720 >= 500000 && v.VideoBitrate720 <= 8000000 && v.VideoBitrate1080 >= v.VideoBitrate720 && v.VideoBitrate1080 <= 8000000
	}
	return true
}

func validState(v JournalState) bool {
	if v.SchemaVersion != 1 || !validUUID(v.OperationID) || !validIntent(v.Intent) || v.PublishExpectedVersion < 0 || v.PackageBytes < 0 || v.PackageBytes > media.RecordingPackageMaxBytes || v.GeneratedOwned && v.Intent.Command != "prepare" && (v.Intent.Command != "upload" || !v.Intent.Prepare) {
		return false
	}
	if v.LastResult != nil && (v.LastResult.OperationID != v.OperationID || v.LastResult.RecordingID != v.RecordingID || v.LastResult.PackageID != v.PackageID || v.LastResult.PackageDigest != v.PackageDigest) {
		return false
	}
	if v.RecordingID != "" && !validUUID(v.RecordingID) {
		return false
	}
	for _, id := range []string{v.PackageID, v.SessionID} {
		if id != "" && !packageIDPattern.MatchString(id) {
			return false
		}
	}
	return true
}
