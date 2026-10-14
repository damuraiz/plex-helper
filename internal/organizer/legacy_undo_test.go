package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

func legacyMovedFixture(t *testing.T) (*Engine, JournalEntry, string) {
	t.Helper()
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "Example.2024.mkv"), "original contents")
	plan := moviePlan(t, e, source, library)
	j, journalPath := interruptedJournal(t, e, plan)
	entry := j.Entries[0]
	if _, err := fastMove(entry.Source, entry.Destination, entry.Identity); err != nil {
		t.Fatal(err)
	}
	return e, entry, journalPath
}

func TestLegacyUndoOneChecksumThenMetadataMove(t *testing.T) {
	for _, alreadyReturned := range []bool{false, true} {
		e, entry, path := legacyMovedFixture(t)
		if alreadyReturned {
			if err := os.Rename(entry.Destination, entry.Source); err != nil {
				t.Fatal(err)
			}
		}
		passes := 0
		proof, ok, err := verifyLegacyUndo(entry, func(path string, done, total int64, phase string) error {
			if phase == "Синхронизация файла" {
				passes++
			}
			return nil
		})
		if err != nil || !ok || passes != 1 {
			t.Fatalf("proof=%+v ok=%v passes=%d err=%v", proof, ok, passes, err)
		}
		if err = proof.restore(entry); err != nil {
			t.Fatal(err)
		}
		if content, err := os.ReadFile(entry.Source); err != nil || string(content) != "original contents" {
			t.Fatal(string(content), err)
		}
		// Exercise the full durable Undo path after an interrupted/manual return.
		j, err := e.Undo(filepath.Base(path)[:32])
		if err != nil || j.Status != "undone" || j.Entries[0].Status != "restored" {
			t.Fatal(j, err)
		}
	}
}

func TestLegacyUndoProofRejectsChangesAndNewConflict(t *testing.T) {
	for _, mode := range []string{"modified", "occupied", "stage"} {
		t.Run(mode, func(t *testing.T) {
			_, entry, _ := legacyMovedFixture(t)
			proof, ok, err := verifyLegacyUndo(entry)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			switch mode {
			case "modified":
				put(t, entry.Destination, "modified contents")
			case "occupied":
				put(t, entry.Source, "other file")
			case "stage":
				put(t, entry.UndoStage, "other file")
			}
			if err = proof.restore(entry); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			if _, err = os.Stat(entry.Destination); err != nil {
				t.Fatal("destination lost", err)
			}
		})
	}
}

func TestLegacyUndoRejectsChangedContentsAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		_, entry, _ := legacyMovedFixture(t)
		callback := fileProgress(func(string, int64, int64, string) error { return nil })
		if cancel {
			callback = func(string, int64, int64, string) error { return ErrStopRequested }
		} else {
			put(t, entry.Destination, "modified contents")
		}
		_, _, err := verifyLegacyUndo(entry, callback)
		if err == nil || cancel && err != ErrStopRequested {
			t.Fatal(err)
		}
		if _, err = os.Stat(entry.Destination); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUndoProgressCountsOnlyRecoverableEntries(t *testing.T) {
	e, entry, _ := legacyMovedFixture(t)
	j := Journal{ID: newID(), Entries: []JournalEntry{entry, {Status: "planned"}, {Status: "restored", Digest: entry.Digest}}}
	e.beginProgress(j, "undo")
	defer e.endProgress()
	e.publish(j, 0, "Проверка")
	p := e.Progress()
	if p.FileCount != 1 || p.FileIndex != 1 || p.Completed != 0 {
		t.Fatal(p)
	}
	j.Entries[0].Status = "restored"
	e.publish(j, 0, "Возвращено")
	if p = e.Progress(); p.FileCount != 1 || p.Completed != 1 {
		t.Fatal(p)
	}
}
