package recordings

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/media"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

const journalFixtureID = "00000000-0000-4000-8000-000000000031"

func TestJournalPinsValidatedEncodingSummary(t *testing.T) {
	intent := journalIntent(t)
	j, err := OpenJournal(t.TempDir(), journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	state := j.State()
	state.Encoding = &media.EncodingSummary{ActualEncoder: "untrusted arbitrary output", PresetVersion: "cpu-hq-v1"}
	if err := j.Save(state); err == nil {
		t.Fatal("accepted unvalidated encoding diagnostics")
	}
	state.Encoding = &media.EncodingSummary{ActualEncoder: "libx264", PresetVersion: "cpu-hq-v1", CPUFallback: true}
	if err := j.Save(state); err != nil {
		t.Fatal(err)
	}
	state.Encoding = nil
	if err := j.Save(state); err == nil {
		t.Fatal("lost completed encoder evidence")
	}
}

func journalIntent(t *testing.T) Intent {
	return Intent{Command: "upload", Profile: "uploader", PrincipalType: "service", PrincipalID: "00000000-0000-4000-8000-000000000011", ClientID: "client", Input: filepath.Join(t.TempDir(), "recording.mkv"), Title: "主日聚會", Prepare: true, Publish: true, VideoBitrate720: 1500000, VideoBitrate1080: 3000000}
}

func TestJournalSavedStateSurvivesProcessExit(t *testing.T) {
	if base := os.Getenv("HHC_JOURNAL_CRASH_FIXTURE"); base != "" {
		intent := journalIntent(t)
		j, err := OpenJournal(base, journalFixtureID, &intent)
		if err != nil {
			t.Fatal(err)
		}
		state := j.State()
		state.RecordingID = "00000000-0000-4000-8000-000000000041"
		if err := j.Save(state); err != nil {
			t.Fatal(err)
		}
		os.Exit(23) // Deliberately bypass Close: the OS must release the lock.
	}
	base := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestJournalSavedStateSurvivesProcessExit$")
	child.Env = append(os.Environ(), "HHC_JOURNAL_CRASH_FIXTURE="+base)
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("fixture did not reach saved state: %v", err)
	}
	j, err := OpenJournal(base, journalFixtureID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j.State().RecordingID != "00000000-0000-4000-8000-000000000041" {
		t.Fatal("lost acknowledged save")
	}
}

func TestJournalRefusesSymlinkWithoutChangingTarget(t *testing.T) {
	base, intent := t.TempDir(), journalIntent(t)
	directory := filepath.Join(base, journalFixtureID)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "preserve.json")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "journal.json")); err != nil {
		t.Skip("symlink fixture unavailable on this OS context")
	}
	if j, err := OpenJournal(base, journalFixtureID, &intent); err == nil {
		j.Close()
		t.Fatal("accepted symlink journal")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "preserve" {
		t.Fatal("modified symlink target")
	}
}

func TestJournalResumesOriginalIntentAndRejectsIdentityOrPublishChanges(t *testing.T) {
	base, intent := t.TempDir(), journalIntent(t)
	j, err := OpenJournal(base, journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(base, journalFixtureID, &intent); !errors.Is(err, operation.ErrOperationBusy) {
		t.Fatalf("concurrent operation: %v", err)
	}
	state := j.State()
	state.RecordingID = "00000000-0000-4000-8000-000000000041"
	state.PackageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := j.Save(state); err != nil {
		t.Fatal(err)
	}
	j.Close()
	resumed, err := OpenJournal(base, journalFixtureID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State().RecordingID != state.RecordingID || resumed.State().Intent != intent {
		t.Fatal("lost durable intent or recording")
	}
	resumed.Close()
	for _, change := range []string{"principal", "publish", "title"} {
		changed := intent
		switch change {
		case "principal":
			changed.PrincipalID = "00000000-0000-4000-8000-000000000099"
		case "publish":
			changed.Publish = false
		case "title":
			changed.Title = "other"
		}
		if next, err := OpenJournal(base, journalFixtureID, &changed); !errors.Is(err, ErrOperationConflict) {
			if next != nil {
				next.Close()
			}
			t.Fatalf("changed intent accepted: %s %v", change, err)
		}
	}
}

func TestJournalInvalidSavePreservesPreviousAndUnknownSchemaIsNotRecreated(t *testing.T) {
	base, intent := t.TempDir(), journalIntent(t)
	j, err := OpenJournal(base, journalFixtureID, &intent)
	if err != nil {
		t.Fatal(err)
	}
	state := j.State()
	state.Intent.Publish = false
	if err := j.Save(state); !errors.Is(err, ErrOperationConflict) {
		t.Fatal("save changed publication intent")
	}
	j.Close()
	path := filepath.Join(base, journalFixtureID, "journal.json")
	before, _ := os.ReadFile(path)
	if len(before) == 0 {
		t.Fatal("lost original journal")
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if next, err := OpenJournal(base, journalFixtureID, &intent); !errors.Is(err, ErrJournalSchema) {
		if next != nil {
			next.Close()
		}
		t.Fatalf("unknown schema recreated: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != `{"schemaVersion":999}` {
		t.Fatal("replaced future journal")
	}
}

func TestJournalMissingResumeDoesNotCreateNewOperation(t *testing.T) {
	base := t.TempDir()
	if j, err := OpenJournal(base, journalFixtureID, nil); err == nil {
		j.Close()
		t.Fatal("invented missing operation")
	}
	if _, err := os.Stat(filepath.Join(base, journalFixtureID)); !os.IsNotExist(err) {
		t.Fatal("missing resume created directory")
	}
}
