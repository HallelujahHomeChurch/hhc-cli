package recordings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepExpiredSkipsActiveAndRecentOperations(t *testing.T) {
	for _, mode := range []string{"expired", "active", "recent", "session-expired"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			intent := journalIntent(t)
			j, err := OpenJournal(base, journalFixtureID, &intent)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := j.generatedWorkspace()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "partial"), []byte("temporary"), 0600); err != nil {
				t.Fatal(err)
			}
			state := j.State()
			now := time.Now().UTC()
			state.UpdatedAt = now.Add(-25 * time.Hour)
			if mode == "recent" || mode == "session-expired" {
				state.UpdatedAt = now
			}
			if mode == "session-expired" {
				state.SessionExpiresAt = now.Add(-time.Minute)
			}
			data, _ := json.Marshal(state)
			if err := os.WriteFile(filepath.Join(j.directory, "journal.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "active" {
				j.Close()
			}
			defer j.Close()
			report, err := SweepExpired(base, now)
			if err != nil || report.Failed != 0 {
				t.Fatalf("sweep %+v %v", report, err)
			}
			_, statErr := os.Stat(directory)
			if mode == "expired" || mode == "session-expired" {
				if report.Removed != 1 || !os.IsNotExist(statErr) {
					t.Fatal("expired bytes remain")
				}
			} else if report.Removed != 0 || statErr != nil {
				t.Fatal("cleaned active/recent bytes")
			}
			if _, err := os.Stat(filepath.Join(base, journalFixtureID, "journal.json")); err != nil {
				t.Fatal("deleted resume receipt")
			}
		})
	}
}
