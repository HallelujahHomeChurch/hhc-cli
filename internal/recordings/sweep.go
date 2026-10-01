package recordings

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type SweepResult struct{ Removed, Failed int }

// SweepExpired is bounded-memory, best-effort startup maintenance, not a daemon.
// Only valid owned journals with an acquired OS lock authorize removal.
func SweepExpired(base string, now time.Time) (report SweepResult, err error) {
	if !filepath.IsAbs(base) {
		return report, ErrInvalidJournal
	}
	info, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return report, ErrInvalidJournal
	}
	entries, err := os.Open(base)
	if err != nil {
		return report, err
	}
	defer entries.Close()
	for {
		batch, readErr := entries.ReadDir(64)
		for _, entry := range batch {
			if !validUUID(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			j, err := OpenJournal(base, entry.Name(), nil)
			if err != nil {
				continue
			} // Busy, corrupt or unknown-schema data is not ours to delete.
			state := j.State()
			expired := !state.UpdatedAt.IsZero() && !now.Before(state.UpdatedAt.Add(24*time.Hour))
			if !state.SessionExpiresAt.IsZero() && !now.Before(state.SessionExpiresAt) {
				expired = true
			}
			if state.GeneratedOwned && expired {
				parent, name, pathErr := j.workspaceLocation()
				if pathErr != nil {
					report.Failed++
				} else if _, statErr := os.Lstat(filepath.Join(parent, name)); !errors.Is(statErr, os.ErrNotExist) {
					if err := j.cleanGenerated(); err != nil {
						report.Failed++
					} else {
						report.Removed++
					}
				}
			}
			j.Close()
		}
		if readErr == io.EOF {
			return report, nil
		}
		if readErr != nil {
			return report, readErr
		}
	}
}
