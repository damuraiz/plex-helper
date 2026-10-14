package organizer

import (
	"github.com/damuraiz/plex-helper/internal/media"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFastMoveLargeSparseFileAndUndoWithoutContentReads(t *testing.T) {
	e, source, library := fixture(t)
	path := filepath.Join(source, "Large.2024.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(1 << 40); err != nil {
		t.Fatal(err)
	}
	f.Close()
	plan := moviePlan(t, e, source, library)
	started := time.Now()
	j, err := e.Apply(plan, []string{plan.Entries[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	entry := j.Entries[0]
	if entry.Verification != "stat" || entry.Digest != "" || entry.DestinationIdentity == nil {
		t.Fatal("same-volume file was hashed", entry)
	}
	if _, err = e.Undo(j.ID); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("metadata-only rename read the 1 TiB fixture")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 1<<40 {
		t.Fatal(info, err)
	}
}

func TestFastUndoRefusesInodeReplacementAndOccupiedOriginal(t *testing.T) {
	for _, mode := range []string{"replace", "occupied"} {
		t.Run(mode, func(t *testing.T) {
			e, source, library := fixture(t)
			path := filepath.Join(source, "Example.2024.mkv")
			put(t, path, "original")
			plan := moviePlan(t, e, source, library)
			j, err := e.Apply(plan, []string{plan.Entries[0].ID})
			if err != nil {
				t.Fatal(err)
			}
			dest := j.Entries[0].Destination
			if mode == "replace" {
				os.Remove(dest)
				put(t, dest, "replaced")
			} else {
				put(t, path, "other")
			}
			if _, err = e.Undo(j.ID); err == nil {
				t.Fatal("unsafe undo accepted")
			}
			if _, err = os.Stat(dest); err != nil {
				t.Fatal("destination was lost", err)
			}
		})
	}
}

func TestJournalAndStopDoNotWaitForOperationMutex(t *testing.T) {
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "Example.2024.mkv"), "fixture")
	plan := moviePlan(t, e, source, library)
	j := Journal{ID: newID(), Root: source, Library: library, Status: "applying", Entries: []JournalEntry{{Entry: plan.Entries[0], Status: "planned"}}}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.beginProgress(j, "apply")
	defer e.endProgress()
	done := make(chan error, 1)
	go func() { _, err := e.Journals(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("journal waited for media mutex")
	}
	if !e.RequestStop() || !e.Progress().StopRequested {
		t.Fatal("stop request blocked")
	}
	if err := e.fileProgress("fixture", 100, 1000, "hash"); err != ErrStopRequested {
		t.Fatal("stop not honoured", err)
	}
}

func TestPlexTypeFolderDoesNotRepeat(t *testing.T) {
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "Dark.S01E01.mkv"), "fixture")
	tvRoot := filepath.Join(library, "TV Shows")
	os.Mkdir(tvRoot, 0700)
	inv, err := e.Scan(source, "auto")
	if err != nil {
		t.Fatal(err)
	}
	selections := map[string]media.Metadata{inv.Items[0].ID: {Kind: "tv", Title: "Dark", Year: 2017}}
	plan, err := e.Plan(inv, tvRoot, selections)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Entries[0].Destination, "TV Shows/TV Shows") {
		t.Fatal(plan.Entries[0].Destination)
	}
}

func TestHashCanStopBeforeReadingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture")
	put(t, path, "fixture")
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(path, nil, func(path string, done, total int64, phase string) error { return ErrStopRequested })
	if err != ErrStopRequested || digest != "" {
		t.Fatal(digest, err)
	}
}

func TestFastCrashWithoutDestinationFingerprintRefusesUndo(t *testing.T) {
	e, source, library := fixture(t)
	path := filepath.Join(source, "Example.2024.mkv")
	put(t, path, "fixture")
	plan := moviePlan(t, e, source, library)
	entry := JournalEntry{Entry: plan.Entries[0], Status: "pending", Verification: "stat"}
	if _, err := fastMove(entry.Source, entry.Destination, entry.Identity); err != nil {
		t.Fatal(err)
	}
	if err := checkFastRestore(entry); err == nil {
		t.Fatal("crash without durable post-move fingerprint was guessed")
	}
	if _, err := os.Stat(entry.Destination); err != nil {
		t.Fatal(err)
	}
}
